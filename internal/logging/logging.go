package logging

import (
	"os"
	"strings"

	"github.com/chu/3xui-user-sync/internal/config"
	"github.com/rs/zerolog"
)

func New(cfg config.Config) zerolog.Logger {
	level, err := zerolog.ParseLevel(strings.ToLower(cfg.LogLevel))
	if err != nil {
		level = zerolog.InfoLevel
	}
	zerolog.SetGlobalLevel(level)

	if strings.EqualFold(cfg.LogFormat, "json") {
		return zerolog.New(os.Stdout).With().Timestamp().Logger()
	}

	writer := zerolog.ConsoleWriter{
		Out:        os.Stdout,
		TimeFormat: "2006-01-02 15:04:05",
	}
	return zerolog.New(writer).With().Timestamp().Logger()
}
