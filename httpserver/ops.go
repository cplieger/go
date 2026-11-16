package httpserver

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"time"
)

var opsBindRetry = struct{ interval, window time.Duration }{250 * time.Millisecond, 5 * time.Second}

func OpsAddr(value, def string) string { return value }

type OpsConfig struct {
	Metrics http.Handler
	Healthz http.Handler
	Errors  ErrorResponder
	Logger  *slog.Logger
	Setting string
	Addr    string
	Hosts   []string
}

type Ops struct {
	srv *http.Server
	ln  net.Listener
}

func ListenOps(_ context.Context, _ OpsConfig) (*Ops, error) { return &Ops{}, nil }

func (o *Ops) Addr() net.Addr { return nil }

type Served struct {
	Server   *http.Server
	Listener net.Listener
	Options  []RunOption
}

func RunWithOps(_ context.Context, _ *Ops, _ ...Served) error { return nil }

type ListenSetting struct{ Name, Addr string }

func CheckListenAddrs(_ ...ListenSetting) error { return nil }
