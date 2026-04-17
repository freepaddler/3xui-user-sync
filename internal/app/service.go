package app

import (
	"encoding/base64"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/chu/3xui-user-sync/internal/config"
	"github.com/chu/3xui-user-sync/internal/domain"
	"github.com/chu/3xui-user-sync/internal/security"
	"github.com/chu/3xui-user-sync/internal/store"
	"github.com/chu/3xui-user-sync/internal/xui"
	"github.com/rs/zerolog"
)

type Service struct {
	cfg      config.Config
	log      zerolog.Logger
	users    *store.UserRepository
	servers  *store.ServerRepository
	sessions *security.SessionStore
	xui      *xui.Client
}

var ErrUpstreamSubscriptionsFailed = errors.New("all upstream subscriptions failed")

type DuplicateEmailError struct {
	ServerID   int64
	ServerName string
	InboundID  int64
	Email      string
	Message    string
}

func (e *DuplicateEmailError) Error() string {
	if strings.TrimSpace(e.Message) != "" {
		return e.Message
	}
	return "Duplicate email: " + e.Email
}

func NewService(
	cfg config.Config,
	log zerolog.Logger,
	users *store.UserRepository,
	servers *store.ServerRepository,
	sessions *security.SessionStore,
	xuiClient *xui.Client,
) *Service {
	return &Service{
		cfg:      cfg,
		log:      log,
		users:    users,
		servers:  servers,
		sessions: sessions,
		xui:      xuiClient,
	}
}

func (s *Service) Login(ctx context.Context, username, password string, remember bool) (security.Session, error) {
	if strings.TrimSpace(s.cfg.BootstrapAdminUser) == "" || s.cfg.BootstrapAdminPass == "" {
		return security.Session{}, errors.New("ADMIN_USERNAME and ADMIN_PASSWORD must be set")
	}
	if strings.TrimSpace(username) != strings.TrimSpace(s.cfg.BootstrapAdminUser) || password != s.cfg.BootstrapAdminPass {
		return security.Session{}, errors.New("invalid credentials")
	}
	return s.sessions.Create(ctx, s.cfg.BootstrapAdminUser, s.cfg.RememberTTL, remember)
}

func (s *Service) Session(ctx context.Context, id string) (security.Session, bool) {
	session, ok := s.sessions.Get(ctx, id)
	if ok {
		s.sessions.Touch(ctx, id)
	}
	return session, ok
}

func (s *Service) Logout(ctx context.Context, id string) {
	s.sessions.Delete(ctx, id)
}

func (s *Service) ListUsers(ctx context.Context) ([]domain.User, error) {
	return s.users.List(ctx)
}

func (s *Service) ListServers(ctx context.Context) ([]domain.Server, error) {
	return s.servers.List(ctx)
}

func (s *Service) CreateUser(ctx context.Context, user domain.User, selections []InboundSelection) (domain.User, error) {
	if strings.TrimSpace(user.UID) == "" {
		user.UID = uuid.NewString()
	}
	user.SubscriptionID = strings.TrimSpace(user.SubscriptionID)
	if user.SubscriptionID == "" {
		user.SubscriptionID = randomSubscriptionID()
	}

	created, err := s.users.Create(ctx, user)
	if err != nil {
		return domain.User{}, err
	}

	var errs []string
	for _, selection := range selections {
		if err := s.setUserInboundState(ctx, created, selection.ServerID, selection.InboundID, true); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if len(errs) > 0 {
		return created, fmt.Errorf(strings.Join(errs, "; "))
	}
	return created, nil
}

func (s *Service) UpdateUser(ctx context.Context, user domain.User) error {
	current, err := s.users.GetByID(ctx, user.ID)
	if err != nil {
		return err
	}

	statuses, err := s.FetchServerStatuses(ctx)
	if err != nil {
		s.log.Error().Err(err).Msg("load server statuses for user update")
	}
	for _, status := range statuses {
		if !status.Reachable {
			continue
		}
		creds, err := s.serverCredentials(status.Server)
		if err != nil {
			return wrapServerError(status.Server, err)
		}
		for _, inbound := range status.Inbounds {
			for _, client := range inbound.Clients {
				if client.UID != current.UID {
					continue
				}
				client.Email = user.Username
				client.SubID = user.SubscriptionID
				client.Enable = client.Enable
				client.InboundID = inbound.ID
				if err := s.xui.UpsertClient(ctx, creds, inbound, client); err != nil {
					return wrapServerError(status.Server, err)
				}
			}
		}
	}

	current.Username = user.Username
	current.SubscriptionID = user.SubscriptionID
	return s.users.Update(ctx, current)
}

func (s *Service) DeleteUser(ctx context.Context, id int64) error {
	user, err := s.users.GetByID(ctx, id)
	if err != nil {
		return err
	}

	statuses, err := s.FetchServerStatuses(ctx)
	if err != nil {
		s.log.Error().Err(err).Msg("load server statuses for user delete")
	}
	for _, status := range statuses {
		if !status.Reachable {
			continue
		}
		creds, err := s.serverCredentials(status.Server)
		if err != nil {
			return wrapServerError(status.Server, err)
		}
		for _, inbound := range status.Inbounds {
			for _, client := range inbound.Clients {
				if client.UID != user.UID {
					continue
				}
				if err := s.xui.DeleteClient(ctx, creds, inbound.ID, user.UID); err != nil {
					return wrapServerError(status.Server, err)
				}
			}
		}
	}

	return s.users.Delete(ctx, id)
}

func (s *Service) ToggleUserInbound(ctx context.Context, userID, serverID, inboundID int64, enabled bool) error {
	user, err := s.users.GetByID(ctx, userID)
	if err != nil {
		return err
	}
	return s.setUserInboundState(ctx, user, serverID, inboundID, enabled)
}

func (s *Service) RecreateUserInbound(ctx context.Context, userID, serverID, inboundID int64) error {
	user, err := s.users.GetByID(ctx, userID)
	if err != nil {
		return err
	}
	server, err := s.servers.GetByID(ctx, serverID)
	if err != nil {
		return err
	}
	creds, err := s.serverCredentials(server)
	if err != nil {
		return wrapServerError(server, err)
	}
	inbounds, err := s.xui.ListInbounds(ctx, creds)
	if err != nil {
		return wrapServerError(server, err)
	}

	var target *domain.Inbound
	for i := range inbounds {
		inbound := &inbounds[i]
		if inbound.ID == inboundID {
			target = inbound
		}
		for _, client := range inbound.Clients {
			if inbound.ID == inboundID {
				continue
			}
			if client.UID != user.UID && client.Email != user.Username {
				continue
			}
			if err := s.xui.DeleteClient(ctx, creds, inbound.ID, client.UID); err != nil {
				return wrapServerError(server, err)
			}
		}
	}
	if target == nil {
		return wrapServerError(server, fmt.Errorf("inbound %d not found", inboundID))
	}

	client := domain.RemoteClient{
		UID:       user.UID,
		Email:     user.Username,
		Enable:    true,
		Flow:      "xtls-rprx-vision",
		SubID:     user.SubscriptionID,
		InboundID: inboundID,
	}
	for _, current := range target.Clients {
		if current.UID != user.UID && current.Email != user.Username {
			continue
		}
		client = current
		client.Email = user.Username
		client.SubID = user.SubscriptionID
		client.Enable = true
		break
	}
	if err := s.xui.UpsertClient(ctx, creds, *target, client); err != nil {
		return wrapServerError(server, err)
	}
	return nil
}

func (s *Service) setUserInboundState(ctx context.Context, user domain.User, serverID, inboundID int64, enabled bool) error {
	server, err := s.servers.GetByID(ctx, serverID)
	if err != nil {
		return err
	}
	creds, err := s.serverCredentials(server)
	if err != nil {
		return wrapServerError(server, err)
	}
	inbounds, err := s.xui.ListInbounds(ctx, creds)
	if err != nil {
		return wrapServerError(server, err)
	}

	for _, inbound := range inbounds {
		if inbound.ID != inboundID {
			continue
		}
		client := domain.RemoteClient{
			UID:       user.UID,
			Email:     user.Username,
			Enable:    enabled,
			Flow:      "xtls-rprx-vision",
			SubID:     user.SubscriptionID,
			InboundID: inboundID,
		}

		found := false
		for _, current := range inbound.Clients {
			if current.UID != user.UID {
				continue
			}
			found = true
			client = current
			client.Email = user.Username
			client.SubID = user.SubscriptionID
			client.Enable = enabled
			break
		}

		if !enabled && !found {
			return nil
		}
		if err := s.xui.UpsertClient(ctx, creds, inbound, client); err != nil {
			if duplicateEmail := parseDuplicateEmail(err); duplicateEmail != "" {
				return &DuplicateEmailError{
					ServerID:   server.ID,
					ServerName: serverLabel(server),
					InboundID:  inbound.ID,
					Email:      duplicateEmail,
					Message:    "Duplicate email: " + duplicateEmail,
				}
			}
			return wrapServerError(server, err)
		}
		return nil
	}

	return wrapServerError(server, fmt.Errorf("inbound %d not found", inboundID))
}

func (s *Service) CreateServer(ctx context.Context, server domain.Server, panelPassword string) (domain.Server, error) {
	enc, err := security.Encrypt(panelPassword)
	if err != nil {
		return domain.Server{}, err
	}
	server.PanelPasswordEnc = enc
	return s.servers.Create(ctx, server)
}

func (s *Service) UpdateServer(ctx context.Context, server domain.Server, panelPassword string) error {
	current, err := s.servers.GetByID(ctx, server.ID)
	if err != nil {
		return err
	}
	if strings.TrimSpace(panelPassword) == "" {
		server.PanelPasswordEnc = current.PanelPasswordEnc
	} else {
		enc, err := security.Encrypt(panelPassword)
		if err != nil {
			return err
		}
		server.PanelPasswordEnc = enc
	}
	return s.servers.Update(ctx, server)
}

func (s *Service) DeleteServer(ctx context.Context, id int64) error {
	return s.servers.Delete(ctx, id)
}

func (s *Service) FetchServerStatuses(ctx context.Context) ([]domain.ServerStatus, error) {
	servers, err := s.servers.List(ctx)
	if err != nil {
		return nil, err
	}

	results := make([]domain.ServerStatus, len(servers))
	var wg sync.WaitGroup
	for idx, server := range servers {
		if !server.Active {
			continue
		}
		wg.Add(1)
		go func(i int, server domain.Server) {
			defer wg.Done()
			results[i] = s.fetchSingleServerStatus(ctx, server)
		}(idx, server)
	}
	wg.Wait()
	return results, nil
}

func (s *Service) fetchSingleServerStatus(ctx context.Context, server domain.Server) domain.ServerStatus {
	status := domain.ServerStatus{
		Server:        server,
		LastCheckedAt: time.Now().UTC(),
	}
	if !server.Active {
		status.Message = "disabled"
		return status
	}

	creds, err := s.serverCredentials(server)
	if err != nil {
		status.Message = wrapServerError(server, err).Error()
		return status
	}
	inbounds, err := s.xui.ListInbounds(ctx, creds)
	if err != nil {
		status.Message = wrapServerError(server, err).Error()
		return status
	}
	status.Reachable = true
	status.Inbounds = inbounds
	return status
}

func (s *Service) SubscriptionBundle(ctx context.Context, subscriptionID string) (AggregatedSubscription, error) {
	servers, err := s.servers.List(ctx)
	if err != nil {
		return AggregatedSubscription{}, err
	}

	type subscriptionResult struct {
		server   domain.Server
		content  string
		userinfo SubscriptionUserinfo
		err      error
	}

	results := make([]subscriptionResult, len(servers))
	var wg sync.WaitGroup
	for idx, server := range servers {
		wg.Add(1)
		go func(i int, server domain.Server) {
			defer wg.Done()
			content, remoteUserinfo, err := s.fetchServerSubscription(ctx, server, subscriptionID)
			results[i] = subscriptionResult{
				server:   server,
				content:  content,
				userinfo: remoteUserinfo,
				err:      err,
			}
		}(idx, server)
	}
	wg.Wait()

	lines := make([]string, 0)
	userinfo := SubscriptionUserinfo{}
	successCount := 0
	for _, result := range results {
		if result.err != nil {
			s.log.Error().Err(wrapServerError(result.server, result.err)).Msg("fetch remote subscription failed")
			continue
		}
		successCount++
		lines = append(lines, splitSubscriptionLines(result.content)...)
		userinfo.Upload += result.userinfo.Upload
		userinfo.Download += result.userinfo.Download
		userinfo.Total += result.userinfo.Total
		if result.userinfo.Expire > userinfo.Expire {
			userinfo.Expire = result.userinfo.Expire
		}
	}
	if successCount == 0 {
		return AggregatedSubscription{}, ErrUpstreamSubscriptionsFailed
	}
	return AggregatedSubscription{
		Content:        base64.StdEncoding.EncodeToString([]byte(strings.Join(lines, "\n"))),
		ProfileTitle:   s.cfg.ProfileTitle,
		UpdateInterval: 12,
		Userinfo:       userinfo,
	}, nil
}

func (s *Service) serverCredentials(server domain.Server) (xui.ServerCredentials, error) {
	password, err := security.Decrypt(server.PanelPasswordEnc)
	if err != nil {
		return xui.ServerCredentials{}, err
	}
	return xui.ServerCredentials{
		BaseURL:      strings.TrimRight(server.BaseURL, "/"),
		Username:     server.PanelUsername,
		Password:     password,
		Subscription: strings.TrimSpace(server.SubscriptionURL),
		ServerID:     server.ID,
		ServerLabel:  serverLabel(server),
	}, nil
}

type InboundSelection struct {
	ServerID  int64
	InboundID int64
}

type AggregatedSubscription struct {
	Content        string
	ProfileTitle   string
	UpdateInterval int
	Userinfo       SubscriptionUserinfo
}

type SubscriptionUserinfo struct {
	Upload   int64
	Download int64
	Total    int64
	Expire   int64
}

func serverLabel(server domain.Server) string {
	if strings.TrimSpace(server.Name) != "" {
		return server.Name
	}
	return server.BaseURL
}

func wrapServerError(server domain.Server, err error) error {
	return fmt.Errorf("%s: %w", serverLabel(server), err)
}

func parseDuplicateEmail(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	const marker = "Duplicate email:"
	idx := strings.Index(msg, marker)
	if idx == -1 {
		return ""
	}
	email := strings.TrimSpace(msg[idx+len(marker):])
	email = strings.TrimSpace(strings.TrimSuffix(email, "\n"))
	email = strings.Trim(email, "()")
	return email
}

func randomSubscriptionID() string {
	return strings.ReplaceAll(uuid.NewString(), "-", "")[:16]
}

func (s *Service) fetchServerSubscription(ctx context.Context, server domain.Server, subscriptionID string) (string, SubscriptionUserinfo, error) {
	targetURL := strings.TrimSpace(server.SubscriptionURL)
	if targetURL == "" {
		return "", SubscriptionUserinfo{}, fmt.Errorf("subscription url is empty")
	}
	if strings.Contains(targetURL, "{subscription_id}") {
		targetURL = strings.ReplaceAll(targetURL, "{subscription_id}", url.PathEscape(subscriptionID))
	} else {
		targetURL = strings.TrimRight(targetURL, "/") + "/" + url.PathEscape(subscriptionID)
	}

	requestCtx, cancel := context.WithTimeout(ctx, s.cfg.RequestTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, targetURL, nil)
	if err != nil {
		return "", SubscriptionUserinfo{}, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", SubscriptionUserinfo{}, err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", SubscriptionUserinfo{}, err
	}
	if resp.StatusCode >= 400 {
		return "", SubscriptionUserinfo{}, fmt.Errorf("subscription http %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil {
		return "", SubscriptionUserinfo{}, fmt.Errorf("decode subscription body: %w", err)
	}
	return string(decoded), parseSubscriptionUserinfo(resp.Header.Get("subscription-userinfo")), nil
}

func parseSubscriptionUserinfo(v string) SubscriptionUserinfo {
	info := SubscriptionUserinfo{}
	for _, part := range strings.Split(v, ";") {
		part = strings.TrimSpace(part)
		kv := strings.SplitN(part, "=", 2)
		if len(kv) != 2 {
			continue
		}
		num, _ := strconv.ParseInt(strings.TrimSpace(kv[1]), 10, 64)
		switch strings.TrimSpace(kv[0]) {
		case "upload":
			info.Upload = num
		case "download":
			info.Download = num
		case "total":
			info.Total = num
		case "expire":
			info.Expire = num
		}
	}
	return info
}

func splitSubscriptionLines(content string) []string {
	rows := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		row = strings.TrimSpace(row)
		if row == "" {
			continue
		}
		out = append(out, row)
	}
	return out
}
