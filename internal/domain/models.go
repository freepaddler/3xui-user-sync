package domain

import "time"

type User struct {
	ID             int64
	Username       string
	SubscriptionID string
	UID            string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

type Server struct {
	ID               int64
	Name             string
	BaseURL          string
	PanelUsername    string
	PanelPasswordEnc string
	SubscriptionURL  string
	Active           bool
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

type ServerStatus struct {
	Server        Server
	Reachable     bool
	Message       string
	Inbounds      []Inbound
	LastCheckedAt time.Time
}

type Inbound struct {
	ID       int64
	Tag      string
	Remark   string
	Protocol string
	Network  string
	Security string
	Clients  []RemoteClient
}

func (i Inbound) DisplayName() string {
	name := i.Remark
	if name == "" {
		name = i.Tag
	}
	if name == "" {
		name = "inbound"
	}
	label := i.Protocol
	if i.Network != "" {
		label += " " + i.Network
	}
	if i.Security != "" {
		label += " " + i.Security
	}
	if label == "" {
		return name
	}
	return name + " (" + label + ")"
}

type RemoteClient struct {
	UID        string
	Email      string
	Flow       string
	Enable     bool
	InboundID  int64
	ServerID   int64
	SubID      string
	TGID       string
	Comment    string
	Reset      int
	LimitIP    int
	TotalGB    int64
	ExpiryTime int64
	CreatedAt  int64
	UpdatedAt  int64
}
