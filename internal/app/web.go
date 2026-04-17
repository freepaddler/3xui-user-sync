package app

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/a-h/templ"
	"github.com/chu/3xui-user-sync/internal/config"
	"github.com/chu/3xui-user-sync/internal/domain"
	"github.com/chu/3xui-user-sync/internal/security"
	"github.com/chu/3xui-user-sync/internal/store"
	"github.com/rs/zerolog"
)

const sessionCookieName = "xui_sync_session"

type Web struct {
	cfg config.Config
	log zerolog.Logger
	svc *Service
}

func NewWeb(cfg config.Config, log zerolog.Logger, svc *Service) *Web {
	return &Web{cfg: cfg, log: log, svc: svc}
}

func (w *Web) Router() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc(w.route("/login"), w.handleLogin)
	mux.HandleFunc(w.route("/logout"), w.handleLogout)
	mux.HandleFunc(w.route(w.cfg.PublicSubPath), w.handleSubscription)

	mux.Handle(w.route("/"), w.auth(http.HandlerFunc(w.handleUsersPage)))
	mux.Handle(w.route("/users"), w.auth(http.HandlerFunc(w.handleUsersPage)))
	mux.Handle(w.route("/users/table"), w.auth(http.HandlerFunc(w.handleUsersTable)))
	mux.Handle(w.route("/users/create"), w.auth(http.HandlerFunc(w.handleUserCreate)))
	mux.Handle(w.route("/users/update"), w.auth(http.HandlerFunc(w.handleUserUpdate)))
	mux.Handle(w.route("/users/delete"), w.auth(http.HandlerFunc(w.handleUserDelete)))
	mux.Handle(w.route("/users/toggle"), w.auth(http.HandlerFunc(w.handleUserToggle)))
	mux.Handle(w.route("/users/recreate"), w.auth(http.HandlerFunc(w.handleUserRecreate)))
	mux.Handle(w.route("/servers"), w.auth(http.HandlerFunc(w.handleServersPage)))
	mux.Handle(w.route("/servers/create"), w.auth(http.HandlerFunc(w.handleServerCreate)))
	mux.Handle(w.route("/servers/update"), w.auth(http.HandlerFunc(w.handleServerUpdate)))
	mux.Handle(w.route("/servers/delete"), w.auth(http.HandlerFunc(w.handleServerDelete)))

	return w.recoverMiddleware(mux)
}

func (w *Web) handleLogin(rw http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		if _, ok := w.currentSession(r); ok {
			http.Redirect(rw, r, w.route("/users"), http.StatusSeeOther)
			return
		}
		w.renderComponent(rw, r, loginPage(w, r.URL.Query().Get("error")))
	case http.MethodPost:
		if err := r.ParseForm(); err != nil {
			http.Redirect(rw, r, w.route("/login")+"?error="+url.QueryEscape("bad form"), http.StatusSeeOther)
			return
		}
		session, err := w.svc.Login(r.Context(), r.FormValue("username"), r.FormValue("password"), r.FormValue("remember") == "1")
		if err != nil {
			http.Redirect(rw, r, w.route("/login")+"?error="+url.QueryEscape(err.Error()), http.StatusSeeOther)
			return
		}
		http.SetCookie(rw, &http.Cookie{
			Name:     sessionCookieName,
			Value:    security.SignSessionValue(session.ID, session.ExpiresAt),
			Path:     cookiePath(w.cfg.BasePath),
			HttpOnly: true,
			Secure:   w.cfg.SecureCookie,
			SameSite: http.SameSiteLaxMode,
			Expires:  session.ExpiresAt,
		})
		http.Redirect(rw, r, w.route("/users"), http.StatusSeeOther)
	default:
		http.Error(rw, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (w *Web) handleLogout(rw http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(rw, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if cookie, err := r.Cookie(sessionCookieName); err == nil {
		if claims, parseErr := security.ParseSessionValue(cookie.Value); parseErr == nil {
			w.svc.Logout(r.Context(), claims.ID)
		}
	}
	http.SetCookie(rw, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     cookiePath(w.cfg.BasePath),
		HttpOnly: true,
		MaxAge:   -1,
	})
	http.Redirect(rw, r, w.route("/login"), http.StatusSeeOther)
}

func (w *Web) handleUsersPage(rw http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(rw, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	page, err := w.loadUsersPage(r.Context(), queryFlash(r), parseUsersParams(r))
	if err != nil {
		http.Error(rw, err.Error(), http.StatusInternalServerError)
		return
	}
	w.renderComponent(rw, r, usersPage(w, page))
}

func (w *Web) handleUsersTable(rw http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		http.Error(rw, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	page, err := w.loadUsersPage(r.Context(), "", parseUsersParams(r))
	if err != nil {
		http.Error(rw, err.Error(), http.StatusInternalServerError)
		return
	}
	w.renderComponent(rw, r, usersTableFragment(w, page, ""))
}

func (w *Web) handleUserCreate(rw http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(rw, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Redirect(rw, r, w.route("/users")+"?error="+url.QueryEscape("bad form"), http.StatusSeeOther)
		return
	}
	selections := parseInboundSelections(r)
	_, err := w.svc.CreateUser(r.Context(), domain.User{
		Username:       strings.TrimSpace(r.FormValue("username")),
		SubscriptionID: strings.TrimSpace(r.FormValue("subscription_id")),
		UID:            strings.TrimSpace(r.FormValue("uid")),
	}, selections)
	redirectWithError(rw, r, w.usersRedirectTarget(r), "user created", err)
}

func (w *Web) handleUserUpdate(rw http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(rw, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Redirect(rw, r, w.route("/users")+"?error="+url.QueryEscape("bad form"), http.StatusSeeOther)
		return
	}
	id, _ := strconv.ParseInt(r.FormValue("id"), 10, 64)
	err := w.svc.UpdateUser(r.Context(), domain.User{
		ID:             id,
		Username:       strings.TrimSpace(r.FormValue("username")),
		SubscriptionID: strings.TrimSpace(r.FormValue("subscription_id")),
	})
	redirectWithError(rw, r, w.usersRedirectTarget(r), "user updated", err)
}

func (w *Web) handleUserDelete(rw http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(rw, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Redirect(rw, r, w.route("/users")+"?error="+url.QueryEscape("bad form"), http.StatusSeeOther)
		return
	}
	id, _ := strconv.ParseInt(r.FormValue("id"), 10, 64)
	err := w.svc.DeleteUser(r.Context(), id)
	redirectWithError(rw, r, w.usersRedirectTarget(r), "user deleted", err)
}

func (w *Web) handleUserToggle(rw http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(rw, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(rw, err.Error(), http.StatusBadRequest)
		return
	}
	userID, _ := strconv.ParseInt(r.FormValue("user_id"), 10, 64)
	serverID, _ := strconv.ParseInt(r.FormValue("server_id"), 10, 64)
	inboundID, _ := strconv.ParseInt(r.FormValue("inbound_id"), 10, 64)
	enabled := r.FormValue("enabled") == "1"
	params := parseUsersParams(r)

	flash := ""
	if err := w.svc.ToggleUserInbound(r.Context(), userID, serverID, inboundID, enabled); err != nil {
		if conflict, ok := err.(*DuplicateEmailError); ok {
			page, loadErr := w.loadUsersPage(r.Context(), "", params)
			if loadErr != nil {
				http.Error(rw, loadErr.Error(), http.StatusInternalServerError)
				return
			}
			page.Conflict = &DuplicateConflict{
				UserID:     userID,
				ServerID:   serverID,
				InboundID:  inboundID,
				Title:      conflict.Error(),
				ServerName: conflict.ServerName,
			}
			w.renderComponent(rw, r, usersTableFragment(w, page, ""))
			return
		}
		flash = err.Error()
	}
	page, err := w.loadUsersPage(r.Context(), flash, params)
	if err != nil {
		http.Error(rw, err.Error(), http.StatusInternalServerError)
		return
	}
	w.renderComponent(rw, r, usersTableFragment(w, page, flash))
}

func (w *Web) handleUserRecreate(rw http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(rw, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(rw, err.Error(), http.StatusBadRequest)
		return
	}
	userID, _ := strconv.ParseInt(r.FormValue("user_id"), 10, 64)
	serverID, _ := strconv.ParseInt(r.FormValue("server_id"), 10, 64)
	inboundID, _ := strconv.ParseInt(r.FormValue("inbound_id"), 10, 64)
	params := parseUsersParams(r)

	flash := ""
	if err := w.svc.RecreateUserInbound(r.Context(), userID, serverID, inboundID); err != nil {
		flash = err.Error()
	}
	page, err := w.loadUsersPage(r.Context(), flash, params)
	if err != nil {
		http.Error(rw, err.Error(), http.StatusInternalServerError)
		return
	}
	w.renderComponent(rw, r, usersTableFragment(w, page, flash))
}

func (w *Web) handleServersPage(rw http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(rw, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	page, err := w.loadServersPage(r.Context(), queryFlash(r))
	if err != nil {
		http.Error(rw, err.Error(), http.StatusInternalServerError)
		return
	}
	w.renderComponent(rw, r, serversPage(w, page))
}

func (w *Web) handleServerCreate(rw http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(rw, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Redirect(rw, r, w.route("/servers")+"?error="+url.QueryEscape("bad form"), http.StatusSeeOther)
		return
	}
	_, err := w.svc.CreateServer(r.Context(), domain.Server{
		Name:            r.FormValue("name"),
		BaseURL:         r.FormValue("base_url"),
		PanelUsername:   r.FormValue("panel_username"),
		SubscriptionURL: r.FormValue("subscription_url"),
		Active:          r.FormValue("active") == "1",
	}, r.FormValue("panel_password"))
	redirectWithError(rw, r, w.route("/servers"), "server saved", err)
}

func (w *Web) handleServerUpdate(rw http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(rw, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Redirect(rw, r, w.route("/servers")+"?error="+url.QueryEscape("bad form"), http.StatusSeeOther)
		return
	}
	id, _ := strconv.ParseInt(r.FormValue("id"), 10, 64)
	err := w.svc.UpdateServer(r.Context(), domain.Server{
		ID:              id,
		Name:            r.FormValue("name"),
		BaseURL:         r.FormValue("base_url"),
		PanelUsername:   r.FormValue("panel_username"),
		SubscriptionURL: r.FormValue("subscription_url"),
		Active:          r.FormValue("active") == "1",
	}, r.FormValue("panel_password"))
	redirectWithError(rw, r, w.route("/servers"), "server updated", err)
}

func (w *Web) handleServerDelete(rw http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(rw, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Redirect(rw, r, w.route("/servers")+"?error="+url.QueryEscape("bad form"), http.StatusSeeOther)
		return
	}
	id, _ := strconv.ParseInt(r.FormValue("id"), 10, 64)
	err := w.svc.DeleteServer(r.Context(), id)
	redirectWithError(rw, r, w.route("/servers"), "server deleted", err)
}

func (w *Web) handleSubscription(rw http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(rw, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	subID := strings.TrimPrefix(r.URL.Path, w.route(w.cfg.PublicSubPath))
	subID = strings.Trim(subID, "/")
	if subID == "" {
		http.NotFound(rw, r)
		return
	}
	subscription, err := w.svc.SubscriptionBundle(r.Context(), subID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			http.NotFound(rw, r)
			return
		}
		if errors.Is(err, ErrUpstreamSubscriptionsFailed) {
			http.Error(rw, err.Error(), http.StatusBadGateway)
			return
		}
		http.Error(rw, err.Error(), http.StatusBadGateway)
		return
	}
	rw.Header().Set("Content-Type", "text/plain; charset=utf-8")
	rw.Header().Set("Profile-Title", "base64:"+base64.StdEncoding.EncodeToString([]byte(subscription.ProfileTitle)))
	rw.Header().Set("Profile-Update-Interval", strconv.Itoa(subscription.UpdateInterval))
	rw.Header().Set("Subscription-Userinfo", fmt.Sprintf(
		"upload=%d; download=%d; total=%d; expire=%d",
		subscription.Userinfo.Upload,
		subscription.Userinfo.Download,
		subscription.Userinfo.Total,
		subscription.Userinfo.Expire,
	))
	_, _ = io.WriteString(rw, subscription.Content)
}

func (w *Web) loadUsersPage(ctx context.Context, flash string, params UsersParams) (UsersPageData, error) {
	params = params.normalize()
	users, err := w.svc.ListUsers(ctx, params.Query, params.Sort, params.Dir)
	if err != nil {
		return UsersPageData{}, err
	}
	statuses, err := w.svc.FetchServerStatuses(ctx)
	if err != nil {
		return UsersPageData{}, err
	}
	return UsersPageData{
		Flash:    flash,
		Users:    users,
		Statuses: statuses,
		Params:   params,
	}, nil
}

func (w *Web) loadServersPage(ctx context.Context, flash string) (ServersPageData, error) {
	servers, err := w.svc.ListServers(ctx)
	if err != nil {
		return ServersPageData{}, err
	}
	statuses, err := w.svc.FetchServerStatuses(ctx)
	if err != nil {
		return ServersPageData{}, err
	}
	return ServersPageData{
		Flash:    flash,
		Servers:  servers,
		Statuses: statuses,
	}, nil
}

func (w *Web) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		session, ok := w.currentSession(r)
		if !ok {
			http.Redirect(rw, r, w.route("/login"), http.StatusSeeOther)
			return
		}
		ctx := security.ContextWithSession(r.Context(), session.ID)
		next.ServeHTTP(rw, r.WithContext(ctx))
	})
}

func (w *Web) currentSession(r *http.Request) (security.Session, bool) {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil {
		return security.Session{}, false
	}
	claims, err := security.ParseSessionValue(cookie.Value)
	if err != nil {
		return security.Session{}, false
	}
	return w.svc.Session(r.Context(), claims.ID)
}

func (w *Web) recoverMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				w.log.Error().Interface("panic", rec).Str("path", r.URL.Path).Msg("panic recovered")
				http.Error(rw, "internal server error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(rw, r)
	})
}

func (w *Web) renderComponent(rw http.ResponseWriter, r *http.Request, component templ.Component) {
	rw.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := component.Render(r.Context(), rw); err != nil {
		http.Error(rw, err.Error(), http.StatusInternalServerError)
	}
}

func (w *Web) route(path string) string {
	if path == "/" {
		if w.cfg.BasePath == "" {
			return "/"
		}
		return w.cfg.BasePath + "/"
	}
	return w.cfg.BasePath + path
}

type UsersPageData struct {
	Flash    string
	Users    []domain.User
	Statuses []domain.ServerStatus
	Conflict *DuplicateConflict
	Params   UsersParams
}

type ServersPageData struct {
	Flash    string
	Servers  []domain.Server
	Statuses []domain.ServerStatus
}

type DuplicateConflict struct {
	UserID     int64
	ServerID   int64
	InboundID  int64
	Title      string
	ServerName string
}

type UsersParams struct {
	Query string
	Sort  string
	Dir   string
}

func (p UsersParams) normalize() UsersParams {
	p.Query = strings.TrimSpace(p.Query)
	switch p.Sort {
	case "subscription_id":
	default:
		p.Sort = "username"
	}
	if p.Dir != "desc" {
		p.Dir = "asc"
	}
	return p
}

func (p UsersParams) queryString() string {
	p = p.normalize()
	values := url.Values{}
	if p.Query != "" {
		values.Set("q", p.Query)
	}
	if p.Sort != "username" {
		values.Set("sort", p.Sort)
	}
	if p.Dir != "asc" {
		values.Set("dir", p.Dir)
	}
	if encoded := values.Encode(); encoded != "" {
		return "?" + encoded
	}
	return ""
}

func parseUsersParams(r *http.Request) UsersParams {
	values := r.URL.Query()
	if err := r.ParseForm(); err == nil {
		values = r.Form
	}
	return (UsersParams{
		Query: values.Get("q"),
		Sort:  values.Get("sort"),
		Dir:   values.Get("dir"),
	}).normalize()
}

func loginPage(w *Web, flash string) templ.Component {
	return pageShell("3xui-users-sync", w, "login", flash, templ.ComponentFunc(func(ctx context.Context, rw io.Writer) error {
		fmt.Fprintf(rw, `<main class="container narrow"><article class="login-card"><h1>3xui-users-sync</h1><form method="post" action="%s">`, html.EscapeString(w.route("/login")))
		if flash != "" {
			fmt.Fprintf(rw, `<p class="flash error">%s</p>`, html.EscapeString(flash))
		}
		writeString(rw, `<label>Username<input name="username" autocomplete="username" required></label>`)
		writeString(rw, `<label>Password<input type="password" name="password" autocomplete="current-password" required></label>`)
		writeString(rw, `<label><input type="checkbox" name="remember" value="1"> Remember</label>`)
		writeString(rw, `<button type="submit">Login</button></form></article></main>`)
		return nil
	}))
}

func usersPage(w *Web, data UsersPageData) templ.Component {
	return pageShell("Users", w, "users", data.Flash, templ.ComponentFunc(func(ctx context.Context, rw io.Writer) error {
		writeString(rw, `<main class="container">`)
		writeString(rw, `<section class="toolbar">`)
		writeString(rw, `<div><h1>Users</h1></div>`)
		fmt.Fprintf(rw, `<div class="toolbar-actions toolbar-users"><form class="users-filters" hx-get="%s" hx-target="#users-table-container" hx-swap="outerHTML" hx-trigger="input changed delay:300ms from:input[name='q'], search, submit"><input type="search" name="q" value="%s" placeholder="Search user or sub id">%s</form><button type="button" class="action-btn primary" onclick="openDialog('user-create')">Add User</button><button class="action-btn contrast" hx-get="%s%s" hx-target="#users-table-container" hx-swap="outerHTML">Reload / Sync</button></div>`,
			html.EscapeString(w.route("/users/table")),
			html.EscapeString(data.Params.Query),
			usersSortHiddenInputs(data.Params),
			html.EscapeString(w.route("/users/table")),
			html.EscapeString(data.Params.queryString()),
		)
		writeString(rw, `</section>`)
		if err := usersTableFragment(w, data, data.Flash).Render(ctx, rw); err != nil {
			return err
		}
		if err := userCreateDialog(w, data).Render(ctx, rw); err != nil {
			return err
		}
		for _, user := range data.Users {
			if err := userEditDialog(w, user, data.Params).Render(ctx, rw); err != nil {
				return err
			}
			if err := userDeleteDialog(w, user, data.Params).Render(ctx, rw); err != nil {
				return err
			}
		}
		writeString(rw, `</main>`)
		return nil
	}))
}

func usersTableFragment(w *Web, data UsersPageData, flash string) templ.Component {
	return templ.ComponentFunc(func(ctx context.Context, rw io.Writer) error {
		writeString(rw, `<div id="users-table-container">`)
		fmt.Fprintf(rw, `<div id="flash">%s</div>`, flashHTML(flash))
		writeString(rw, `<div class="table-scroll"><table class="users-table"><thead>`)
		fmt.Fprintf(rw, `<tr><th rowspan="2" class="col-user sortable-header"><a href="#" hx-get="%s%s" hx-target="#users-table-container" hx-swap="outerHTML" onclick="return false;">User %s</a></th><th rowspan="2" class="col-sub-id sortable-header"><a href="#" hx-get="%s%s" hx-target="#users-table-container" hx-swap="outerHTML" onclick="return false;">Sub ID %s</a></th>`,
			html.EscapeString(w.route("/users/table")),
			html.EscapeString(nextUsersSortParams(data.Params, "username").queryString()),
			html.EscapeString(sortIndicator(data.Params, "username")),
			html.EscapeString(w.route("/users/table")),
			html.EscapeString(nextUsersSortParams(data.Params, "subscription_id").queryString()),
			html.EscapeString(sortIndicator(data.Params, "subscription_id")),
		)
		for _, status := range data.Statuses {
			colspan := len(status.Inbounds)
			if colspan == 0 {
				colspan = 1
			}
			fmt.Fprintf(rw, `<th colspan="%d" class="%s">%s</th>`, colspan, serverClass(status), html.EscapeString(serverLabel(status.Server)))
		}
		writeString(rw, `<th rowspan="2">Actions</th></tr><tr>`)
		for _, status := range data.Statuses {
			if len(status.Inbounds) == 0 {
				label := "⛔"
				title := "unavailable"
				if status.Reachable {
					label = "–"
					title = "no inbounds"
				}
				fmt.Fprintf(rw, `<th class="%s" title="%s">%s</th>`, serverClass(status), html.EscapeString(title), label)
				continue
			}
			for _, inbound := range status.Inbounds {
				fmt.Fprintf(rw, `<th class="%s" title="%s">%s</th>`,
					serverClass(status),
					html.EscapeString(inboundTypeLabel(inbound)),
					html.EscapeString(inboundShortLabel(inbound)),
				)
			}
		}
		writeString(rw, `</tr></thead><tbody>`)
		for _, user := range data.Users {
			fmt.Fprintf(rw, `<tr><td class="col-user"><strong>%s</strong></td><td class="col-sub-id"><code>%s</code></td>`, html.EscapeString(user.Username), html.EscapeString(user.SubscriptionID))
			for _, status := range data.Statuses {
				if len(status.Inbounds) == 0 {
					fmt.Fprintf(rw, `<td class="%s muted">-</td>`, serverClass(status))
					continue
				}
				for _, inbound := range status.Inbounds {
					checked, disabled := userInboundState(user, status, inbound)
					fmt.Fprintf(rw, `<td class="%s"><form hx-post="%s" hx-target="#users-table-container" hx-swap="outerHTML"><input type="hidden" name="user_id" value="%d"><input type="hidden" name="server_id" value="%d"><input type="hidden" name="inbound_id" value="%d">%s<label><input type="checkbox" name="enabled" value="1" %s %s onchange="this.form.requestSubmit()"></label></form></td>`,
						serverClass(status),
						html.EscapeString(w.route("/users/toggle")),
						user.ID,
						status.Server.ID,
						inbound.ID,
						usersParamsHiddenInputs(data.Params),
						checkedAttr(checked),
						disabledAttr(disabled),
					)
				}
			}
			fmt.Fprintf(rw, `<td class="actions"><div class="actions"><button type="button" class="action-btn" onclick="openDialog('user-edit-%d')">Edit</button><button type="button" class="action-btn danger-btn" onclick="openDialog('user-delete-%d')">Delete</button></div></td></tr>`, user.ID, user.ID)
		}
		if len(data.Users) == 0 {
			writeString(rw, `<tr><td colspan="99" class="muted">No users yet.</td></tr>`)
		}
		writeString(rw, `</tbody></table></div></div>`)
		if data.Conflict != nil {
			if err := duplicateConflictDialog(w, *data.Conflict, data.Params).Render(ctx, rw); err != nil {
				return err
			}
			writeString(rw, `<script>openDialog('duplicate-conflict')</script>`)
		}
		return nil
	})
}

func duplicateConflictDialog(w *Web, conflict DuplicateConflict, params UsersParams) templ.Component {
	return dialog("duplicate-conflict", conflict.Title, templ.ComponentFunc(func(ctx context.Context, rw io.Writer) error {
		fmt.Fprintf(rw, `<form hx-post="%s" hx-target="#users-table-container" hx-swap="outerHTML"><input type="hidden" name="user_id" value="%d"><input type="hidden" name="server_id" value="%d"><input type="hidden" name="inbound_id" value="%d">%s<p>User already exists in another inbound on server <strong>%s</strong>.</p><footer class="dialog-actions split"><button type="button" class="action-btn outline" onclick="closeDialog(this)">Cancel</button><button type="submit" class="action-btn danger-btn" onclick="closeDialog(this)">Recreate</button></footer></form>`,
			html.EscapeString(w.route("/users/recreate")),
			conflict.UserID,
			conflict.ServerID,
			conflict.InboundID,
			usersParamsHiddenInputs(params),
			html.EscapeString(conflict.ServerName),
		)
		return nil
	}))
}

func userCreateDialog(w *Web, data UsersPageData) templ.Component {
	return dialog("user-create", "Add user", templ.ComponentFunc(func(ctx context.Context, rw io.Writer) error {
		fmt.Fprintf(rw, `<form method="post" action="%s">%s<label>Username<input name="username" required></label><label>Subscription ID<input name="subscription_id" placeholder="auto-generate if empty"></label><label>UID<input name="uid" placeholder="auto-generate if empty"></label><fieldset><legend>Initial inbounds</legend>`, html.EscapeString(w.route("/users/create")), usersParamsHiddenInputs(data.Params))
		for _, status := range data.Statuses {
			fmt.Fprintf(rw, `<div class="fieldset-block %s"><strong>%s</strong>`, serverClass(status), html.EscapeString(serverLabel(status.Server)))
			if !status.Reachable {
				fmt.Fprintf(rw, `<p class="flash error">%s</p></div>`, html.EscapeString(status.Message))
				continue
			}
			if len(status.Inbounds) == 0 {
				writeString(rw, `<p class="muted">No inbounds.</p></div>`)
				continue
			}
			for _, inbound := range status.Inbounds {
				fmt.Fprintf(rw, `<label><input type="checkbox" name="select_%d_%d" value="1"> %s</label>`, status.Server.ID, inbound.ID, html.EscapeString(inbound.DisplayName()))
			}
			writeString(rw, `</div>`)
		}
		writeString(rw, `</fieldset><footer class="dialog-actions split"><button type="button" class="action-btn outline" onclick="closeDialog(this)">Cancel</button><button type="submit" class="action-btn">Create</button></footer></form>`)
		return nil
	}))
}

func userEditDialog(w *Web, user domain.User, params UsersParams) templ.Component {
	return dialog(fmt.Sprintf("user-edit-%d", user.ID), "Edit user", templ.ComponentFunc(func(ctx context.Context, rw io.Writer) error {
		fmt.Fprintf(rw, `<form method="post" action="%s"><input type="hidden" name="id" value="%d">%s<label>Subscription URL<div class="copy-row"><input class="subscription-url-input" data-base-path="%s" readonly><button type="button" class="action-btn outline copy-btn" onclick="copySubscriptionURL(this)">Copy</button></div></label><label>Username<input name="username" value="%s" required></label><label>Subscription ID<input name="subscription_id" value="%s" required oninput="updateSubscriptionURL(this.form)"></label><label>UID<input value="%s" readonly disabled></label><footer class="dialog-actions split"><button type="button" class="action-btn outline" onclick="closeDialog(this)">Cancel</button><button type="submit" class="action-btn">Save</button></footer></form>`,
			html.EscapeString(w.route("/users/update")),
			user.ID,
			usersParamsHiddenInputs(params),
			html.EscapeString(w.route(w.cfg.PublicSubPath)),
			html.EscapeString(user.Username),
			html.EscapeString(user.SubscriptionID),
			html.EscapeString(user.UID),
		)
		return nil
	}))
}

func userDeleteDialog(w *Web, user domain.User, params UsersParams) templ.Component {
	return dialog(fmt.Sprintf("user-delete-%d", user.ID), "Delete user", templ.ComponentFunc(func(ctx context.Context, rw io.Writer) error {
		fmt.Fprintf(rw, `<form method="post" action="%s"><input type="hidden" name="id" value="%d">%s<p>Delete <strong>%s</strong> locally and from all reachable 3x-ui servers?</p><footer class="dialog-actions split"><button type="button" class="action-btn outline" onclick="closeDialog(this)">Cancel</button><button type="submit" class="action-btn danger-btn">Delete</button></footer></form>`,
			html.EscapeString(w.route("/users/delete")),
			user.ID,
			usersParamsHiddenInputs(params),
			html.EscapeString(user.Username),
		)
		return nil
	}))
}

func serversPage(w *Web, data ServersPageData) templ.Component {
	return pageShell("Servers", w, "servers", data.Flash, templ.ComponentFunc(func(ctx context.Context, rw io.Writer) error {
		writeString(rw, `<main class="container">`)
		writeString(rw, `<section class="toolbar"><div><h1>Servers</h1><p>Configure remote 3x-ui panels and their subscription endpoints.</p></div><div class="toolbar-actions"><button type="button" class="action-btn" onclick="openDialog('server-create')">Add Server</button></div></section>`)
		writeString(rw, `<div class="table-scroll"><table class="servers-table"><thead><tr><th>Name</th><th>URL</th><th>User</th><th>Sub URL</th><th>Active</th><th>Inbounds</th><th>Actions</th></tr></thead><tbody>`)
		for _, status := range data.Statuses {
			fmt.Fprintf(rw, `<tr class="%s"><td><strong>%s</strong></td><td><code>%s</code></td><td>%s</td><td><code>%s</code></td><td>%s</td><td>`,
				serverClass(status),
				html.EscapeString(serverLabel(status.Server)),
				html.EscapeString(status.Server.BaseURL),
				html.EscapeString(status.Server.PanelUsername),
				html.EscapeString(status.Server.SubscriptionURL),
				yesNo(status.Server.Active),
			)
			if !status.Reachable {
				fmt.Fprintf(rw, `<span class="flash error inline-flash">%s</span>`, html.EscapeString(status.Message))
			} else if len(status.Inbounds) == 0 {
				writeString(rw, `<span class="muted">No inbounds.</span>`)
			} else {
				writeString(rw, `<div class="server-inbounds">`)
				for _, inbound := range status.Inbounds {
					fmt.Fprintf(rw, `<div title="%s">%s</div>`, html.EscapeString(inboundTypeLabel(inbound)), html.EscapeString(inboundShortLabel(inbound)))
				}
				writeString(rw, `</div>`)
			}
			fmt.Fprintf(rw, `</td><td class="actions"><div class="actions"><button type="button" class="action-btn" onclick="openDialog('server-edit-%d')">Edit</button><button type="button" class="action-btn danger-btn" onclick="openDialog('server-delete-%d')">Delete</button></div></td></tr>`, status.Server.ID, status.Server.ID)
		}
		if len(data.Servers) == 0 {
			writeString(rw, `<tr><td colspan="7" class="muted">No servers yet.</td></tr>`)
		}
		writeString(rw, `</tbody></table></div>`)
		if err := serverCreateDialog(w).Render(ctx, rw); err != nil {
			return err
		}
		for _, server := range data.Servers {
			if err := serverEditDialog(w, server).Render(ctx, rw); err != nil {
				return err
			}
			if err := serverDeleteDialog(w, server).Render(ctx, rw); err != nil {
				return err
			}
		}
		writeString(rw, `</main>`)
		return nil
	}))
}

func serverCreateDialog(w *Web) templ.Component {
	return dialog("server-create", "Add server", serverForm(w.route("/servers/create"), domain.Server{}, false))
}

func serverEditDialog(w *Web, server domain.Server) templ.Component {
	return dialog(fmt.Sprintf("server-edit-%d", server.ID), "Edit server", serverForm(w.route("/servers/update"), server, true))
}

func serverDeleteDialog(w *Web, server domain.Server) templ.Component {
	return dialog(fmt.Sprintf("server-delete-%d", server.ID), "Delete server", templ.ComponentFunc(func(ctx context.Context, rw io.Writer) error {
		fmt.Fprintf(rw, `<form method="post" action="%s"><input type="hidden" name="id" value="%d"><p>Delete server <strong>%s</strong>?</p><footer class="dialog-actions split"><button type="button" class="action-btn outline" onclick="closeDialog(this)">Cancel</button><button type="submit" class="action-btn danger-btn">Delete</button></footer></form>`, html.EscapeString(w.route("/servers/delete")), server.ID, html.EscapeString(serverLabel(server)))
		return nil
	}))
}

func serverForm(action string, server domain.Server, isEdit bool) templ.Component {
	return templ.ComponentFunc(func(ctx context.Context, rw io.Writer) error {
		fmt.Fprintf(rw, `<form method="post" action="%s">`, html.EscapeString(action))
		if isEdit {
			fmt.Fprintf(rw, `<input type="hidden" name="id" value="%d">`, server.ID)
		}
		fmt.Fprintf(rw, `<label>Name<input name="name" value="%s"></label><label>Base URL<input type="url" name="base_url" value="%s" required></label><label>Panel username<input name="panel_username" value="%s" required></label><label>Panel password<input type="password" name="panel_password" %s></label><label>Subscription URL<input type="url" name="subscription_url" value="%s" placeholder="https://server.example/3x/sub/{subscription_id}" required></label><label><input type="checkbox" name="active" value="1" %s> Active</label><footer class="dialog-actions split"><button type="button" class="action-btn outline" onclick="closeDialog(this)">Cancel</button><button type="submit" class="action-btn">%s</button></footer></form>`,
			html.EscapeString(server.Name),
			html.EscapeString(server.BaseURL),
			html.EscapeString(server.PanelUsername),
			requiredAttr(!isEdit),
			html.EscapeString(server.SubscriptionURL),
			checkedAttr(server.Active || !isEdit),
			map[bool]string{true: "Save", false: "Create"}[isEdit],
		)
		return nil
	})
}

func pageShell(title string, w *Web, active string, flash string, body templ.Component) templ.Component {
	return templ.ComponentFunc(func(ctx context.Context, rw io.Writer) error {
		writeString(rw, `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><meta name="color-scheme" content="light dark">`)
		fmt.Fprintf(rw, `<title>%s</title>`, html.EscapeString(title))
		writeString(rw, `<link rel="stylesheet" href="https://cdn.jsdelivr.net/npm/@picocss/pico@2/css/pico.min.css">`)
		writeString(rw, `<script src="https://unpkg.com/htmx.org@2.0.4"></script>`)
		writeString(rw, `<style>
html{font-size:15px}
body{padding-bottom:4rem}
nav{margin-bottom:1.5rem}
.container.narrow{max-width:30rem}
.toolbar{display:flex;justify-content:space-between;gap:1rem;align-items:flex-start;flex-wrap:wrap}
.toolbar-actions,.actions,.dialog-actions{display:flex;gap:.75rem;flex-wrap:nowrap;align-items:center}
.toolbar-users{align-items:flex-start;flex-wrap:wrap;justify-content:flex-end;flex:1 1 40rem}
.users-filters{display:flex;gap:.75rem;align-items:center;flex-wrap:wrap;margin:0}
.users-filters input{margin:0}
.users-filters input[type="search"]{min-width:16rem}
.table-scroll{overflow:auto}
.muted{opacity:.6}
.server-offline{background:rgba(128,128,128,.12)}
.server-online{}
.flash{padding:.75rem 1rem;border-radius:.5rem;margin-bottom:1rem}
.flash.error{background:#f8d7da;color:#58151c}
.flash.ok{background:#d1e7dd;color:#0f5132}
dialog{max-width:min(56rem,96vw);border:none}
dialog::backdrop{background:rgba(0,0,0,.4)}
fieldset .fieldset-block{padding:.75rem;border:1px solid var(--muted-border-color);border-radius:.5rem;margin-bottom:.75rem}
code{white-space:nowrap}
.action-btn{display:inline-flex;align-items:center;justify-content:center;min-width:8.5rem;margin:0;border-radius:.8rem;padding:.8rem 1.1rem;white-space:nowrap}
.danger-btn{background:#b42318;border:1px solid #b42318;color:#fff}
.danger-btn:hover{background:#911a12;border-color:#911a12;color:#fff}
.split{justify-content:flex-end}
.nav-links{display:flex;gap:.75rem;align-items:center}
.theme-switcher{min-width:8rem;margin:0}
.nav-link{display:inline-flex;align-items:center;justify-content:center;min-width:8rem;padding:.8rem 1rem;border-radius:.75rem;text-decoration:none}
.nav-link.active{background:var(--pico-primary-background);border:1px solid var(--pico-primary-border);color:var(--pico-primary-inverse)}
.nav-link:not(.active){background:transparent;border:1px solid var(--pico-primary-border);color:var(--pico-primary)}
.nav-form{margin:0}
.login-card{padding:1rem 1rem .5rem}
.login-card h1{margin-bottom:1.5rem}
.table-scroll table td.actions{white-space:nowrap}
.table-scroll table td.actions .actions{flex-wrap:nowrap;justify-content:flex-start}
.table-scroll table td.actions .action-btn{min-width:5.5rem}
.servers-table code{white-space:normal;word-break:break-all}
.server-inbounds{display:grid;gap:.25rem}
.inline-flash{display:inline-block;margin:0;padding:.45rem .65rem}
.users-table .col-user{min-width:12rem;width:12rem}
.users-table .col-sub-id{min-width:13rem;width:13rem}
.users-table .col-sub-id code{display:inline-block;max-width:13rem;overflow:hidden;text-overflow:ellipsis;vertical-align:bottom}
.sortable-header a{color:inherit;text-decoration:none;display:inline-flex;align-items:center;gap:.35rem}
.copy-row{display:flex;gap:.75rem;align-items:center}
.copy-row .subscription-url-input{margin:0}
.copy-row .copy-btn{min-width:6rem}
</style>`)
		writeString(rw, `<script>
const THEME_KEY = 'picoPreferredColorScheme';
function applyTheme(theme){
  const root = document.documentElement;
  if(theme === 'light' || theme === 'dark'){
    root.setAttribute('data-theme', theme);
  } else {
    root.removeAttribute('data-theme');
  }
}
function setTheme(theme){
  localStorage.setItem(THEME_KEY, theme);
  applyTheme(theme);
}
function getTheme(){
  return localStorage.getItem(THEME_KEY) || 'auto';
}
document.addEventListener('DOMContentLoaded', () => {
  const theme = getTheme();
  applyTheme(theme);
  const select = document.getElementById('theme-switcher');
  if(select){
    select.value = theme;
    select.addEventListener('change', (event) => setTheme(event.target.value));
  }
  initSubscriptionURLs(document);
});
document.addEventListener('htmx:afterSwap', (event) => initSubscriptionURLs(event.target));
function initSubscriptionURLs(root){
  root.querySelectorAll?.('.subscription-url-input').forEach((input) => {
    const form = input.closest('form');
    const subIDInput = form?.querySelector('input[name="subscription_id"]');
    const basePath = input.dataset.basePath || '';
    input.value = window.location.origin + basePath + (subIDInput?.value || '');
  });
}
function updateSubscriptionURL(form){
  const input = form.querySelector('.subscription-url-input');
  if(!input) return;
  const subIDInput = form.querySelector('input[name="subscription_id"]');
  const basePath = input.dataset.basePath || '';
  input.value = window.location.origin + basePath + (subIDInput?.value || '');
}
function copySubscriptionURL(button){
  const input = button.parentElement.querySelector('.subscription-url-input');
  if(!input) return;
  navigator.clipboard.writeText(input.value).then(() => {
    const original = button.textContent;
    button.textContent = 'Copied';
    setTimeout(() => { button.textContent = original; }, 1200);
  });
}
function openDialog(id){const el=document.getElementById(id);if(el&&el.showModal){el.showModal();}}
function closeDialog(target){const el=target.closest('dialog');if(el){el.close();}}
</script></head><body>`)
		if active != "login" {
			fmt.Fprintf(rw, `<nav class="container"><ul><li><select id="theme-switcher" class="theme-switcher" aria-label="Color scheme"><option value="auto">Auto</option><option value="light">Light</option><option value="dark">Dark</option></select></li></ul><ul class="nav-links"><li><a href="%s" class="nav-link %s">Users</a></li><li><a href="%s" class="nav-link %s">Servers</a></li><li><form method="post" action="%s" class="nav-form"><button type="submit" class="action-btn secondary">Logout</button></form></li></ul></nav>`,
				html.EscapeString(w.route("/users")), activeClass(active == "users"),
				html.EscapeString(w.route("/servers")), activeClass(active == "servers"),
				html.EscapeString(w.route("/logout")),
			)
		}
		if err := body.Render(ctx, rw); err != nil {
			return err
		}
		writeString(rw, `</body></html>`)
		return nil
	})
}

func dialog(id, title string, body templ.Component) templ.Component {
	return templ.ComponentFunc(func(ctx context.Context, rw io.Writer) error {
		fmt.Fprintf(rw, `<dialog id="%s"><article><header><strong>%s</strong></header>`, html.EscapeString(id), html.EscapeString(title))
		if err := body.Render(ctx, rw); err != nil {
			return err
		}
		writeString(rw, `</article></dialog>`)
		return nil
	})
}

func userInboundState(user domain.User, status domain.ServerStatus, inbound domain.Inbound) (checked bool, disabled bool) {
	if !status.Reachable {
		return false, true
	}
	for _, client := range inbound.Clients {
		if client.UID != user.UID {
			continue
		}
		return client.Enable, false
	}
	return false, false
}

func inboundShortLabel(inbound domain.Inbound) string {
	label := strings.TrimSpace(inbound.Remark)
	if label == "" {
		label = strings.TrimSpace(inbound.Tag)
	}
	if label == "" {
		label = "inbound"
	}
	return label
}

func inboundTypeLabel(inbound domain.Inbound) string {
	parts := []string{}
	if inbound.Protocol != "" {
		parts = append(parts, inbound.Protocol)
	}
	if inbound.Network != "" {
		parts = append(parts, inbound.Network)
	}
	if inbound.Security != "" {
		parts = append(parts, inbound.Security)
	}
	if len(parts) == 0 {
		return inboundShortLabel(inbound)
	}
	return strings.Join(parts, " ")
}

func parseInboundSelections(r *http.Request) []InboundSelection {
	var out []InboundSelection
	for key := range r.PostForm {
		if !strings.HasPrefix(key, "select_") {
			continue
		}
		parts := strings.Split(strings.TrimPrefix(key, "select_"), "_")
		if len(parts) != 2 {
			continue
		}
		serverID, errA := strconv.ParseInt(parts[0], 10, 64)
		inboundID, errB := strconv.ParseInt(parts[1], 10, 64)
		if errA != nil || errB != nil {
			continue
		}
		out = append(out, InboundSelection{ServerID: serverID, InboundID: inboundID})
	}
	return out
}

func usersParamsHiddenInputs(params UsersParams) string {
	params = params.normalize()
	return fmt.Sprintf(
		`<input type="hidden" name="q" value="%s"><input type="hidden" name="sort" value="%s"><input type="hidden" name="dir" value="%s">`,
		html.EscapeString(params.Query),
		html.EscapeString(params.Sort),
		html.EscapeString(params.Dir),
	)
}

func usersSortHiddenInputs(params UsersParams) string {
	params = params.normalize()
	return fmt.Sprintf(
		`<input type="hidden" name="sort" value="%s"><input type="hidden" name="dir" value="%s">`,
		html.EscapeString(params.Sort),
		html.EscapeString(params.Dir),
	)
}

func (w *Web) usersRedirectTarget(r *http.Request) string {
	return w.route("/users") + parseUsersParams(r).queryString()
}

func nextUsersSortParams(params UsersParams, column string) UsersParams {
	params = params.normalize()
	if params.Sort == column {
		if params.Dir == "asc" {
			params.Dir = "desc"
		} else {
			params.Dir = "asc"
		}
		return params
	}
	params.Sort = column
	params.Dir = "asc"
	return params
}

func sortIndicator(params UsersParams, column string) string {
	params = params.normalize()
	if params.Sort != column {
		return ""
	}
	if params.Dir == "desc" {
		return "↓"
	}
	return "↑"
}

func redirectWithError(rw http.ResponseWriter, r *http.Request, location, okText string, err error) {
	target := location
	separator := "?"
	if strings.Contains(target, "?") {
		separator = "&"
	}
	if err != nil {
		target += separator + "error=" + url.QueryEscape(err.Error())
	} else {
		target += separator + "ok=" + url.QueryEscape(okText)
	}
	http.Redirect(rw, r, target, http.StatusSeeOther)
}

func queryFlash(r *http.Request) string {
	if errText := r.URL.Query().Get("error"); errText != "" {
		return errText
	}
	return r.URL.Query().Get("ok")
}

func flashHTML(text string) string {
	if text == "" {
		return ""
	}
	className := "ok"
	if !strings.Contains(strings.ToLower(text), "created") && !strings.Contains(strings.ToLower(text), "updated") && !strings.Contains(strings.ToLower(text), "deleted") && !strings.Contains(strings.ToLower(text), "saved") {
		className = "error"
	}
	return `<p class="flash ` + className + `">` + html.EscapeString(text) + `</p>`
}

func requiredAttr(v bool) string {
	if v {
		return "required"
	}
	return ""
}

func checkedAttr(v bool) string {
	if v {
		return "checked"
	}
	return ""
}

func disabledAttr(v bool) string {
	if v {
		return "disabled"
	}
	return ""
}

func activeClass(v bool) string {
	if v {
		return "active"
	}
	return ""
}

func yesNo(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}

func serverClass(status domain.ServerStatus) string {
	if status.Reachable {
		return "server-online"
	}
	return "server-offline"
}

func cookiePath(basePath string) string {
	if basePath == "" {
		return "/"
	}
	return basePath
}

func writeString(w io.Writer, s string) {
	_, _ = io.WriteString(w, s)
}
