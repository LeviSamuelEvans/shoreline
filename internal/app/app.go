package app

import (
	"context"

	"github.com/levievans/shoreline/internal/auth"
	"github.com/levievans/shoreline/internal/cli"
	"github.com/levievans/shoreline/internal/config"
	"github.com/levievans/shoreline/internal/player"
	"github.com/levievans/shoreline/internal/soundcloud"
)

type App struct {
	config config.Config
	state  *config.StateStore
	auth   *auth.Manager
	sc     *soundcloud.Client
	mpv    *player.MPV
}

func New() (*App, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}

	store := config.NewStateStore(cfg.StatePath)
	secrets := auth.NewSecrets(auth.NewDefaultSecretStore(cfg))
	authManager := auth.NewManager(cfg, store, secrets)
	mpv, _ := player.NewMPV()

	return &App{
		config: cfg,
		state:  store,
		auth:   authManager,
		sc:     soundcloud.NewClient(authManager),
		mpv:    mpv,
	}, nil
}

func (a *App) Run(ctx context.Context, args []string) error {
	root := cli.NewRoot(a.config, a.state, a.auth, a.sc, a.mpv)
	return root.Run(ctx, args)
}
