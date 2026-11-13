package apiv2

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/notifications"
)

type PremiereCapability struct {
	Capability
	Available bool `json:"available"`
}
type PremiereCapabilityOutput struct {
	Status       int
	ETag         string `header:"ETag"`
	CacheControl string `header:"Cache-Control"`
	Body         PremiereCapability
}

func (c PremiereCapability) capabilityState() string { return configuredCapabilityState(c.Available) }

type PremiereChannel struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Group    string `json:"group"`
	Provider string `json:"provider"`
	URL      string `json:"url" format:"uri-reference" doc:"Authenticated server-relative plugin page; never an upstream stream URL."`
}
type PremiereMapping notifications.PremiereMapping
type PremierePreferences notifications.PremierePreferences
type PremiereConfiguration struct {
	Preferences       PremierePreferences `json:"preferences"`
	Mappings          []PremiereMapping   `json:"mappings"`
	Channels          []PremiereChannel   `json:"channels"`
	ChannelsAvailable bool                `json:"channels_available"`
}
type PremiereConfigurationOutput struct{ Body PremiereConfiguration }
type PremiereConfigurationInput struct {
	Body struct {
		Preferences PremierePreferences `json:"preferences"`
		Mappings    []PremiereMapping   `json:"mappings" maxItems:"100"`
	}
}
type PremiereInput struct {
	ItemID string `query:"item_id" maxLength:"512"`
}
type Premiere struct {
	EpisodeID    string            `json:"episode_id"`
	SeriesID     string            `json:"series_id"`
	Title        string            `json:"title"`
	EpisodeTitle string            `json:"episode_title"`
	Season       int               `json:"season"`
	Episode      int               `json:"episode"`
	StartsAt     Instant           `json:"starts_at"`
	EndsAt       Instant           `json:"ends_at"`
	Active       bool              `json:"active"`
	Broadcaster  string            `json:"broadcaster"`
	Channels     []PremiereChannel `json:"channels"`
}
type PremiereOutput struct {
	Body struct {
		Items []Premiere `json:"items"`
	}
}

func registerPremieres(reg *Registry) {
	op := func(method, path, id, summary string) Operation {
		return viewerOperation(humaOp(method, Prefix+"/premieres"+path, id, "home", summary))
	}
	Register(reg, op(http.MethodGet, "/capabilities", "getPremiereCapabilities", "Discover metadata-driven premiere reminders and channel shortcuts."), func(context.Context, *CapabilityInput) (*PremiereCapabilityOutput, error) {
		available := reg.deps.Premieres != nil
		return &PremiereCapabilityOutput{Body: PremiereCapability{Capability: Capability{State: configuredCapabilityState(available)}, Available: available}}, nil
	})
	Register(reg, op(http.MethodGet, "/settings", "getPremiereSettings", "Read this profile's premiere settings, broadcaster mappings and accessible channels."), func(ctx context.Context, _ *CapabilityInput) (*PremiereConfigurationOutput, error) {
		if reg.deps.Premieres == nil {
			return nil, unavailable("premieres")
		}
		user, profile, p := viewerIdentity(ctx)
		if p != nil {
			return nil, p
		}
		cfg, err := reg.deps.Premieres.Configuration(ctx, user, profile)
		if err != nil {
			return nil, serviceProblem(err)
		}
		return &PremiereConfigurationOutput{Body: premiereConfigurationOf(cfg)}, nil
	})
	put := op(http.MethodPut, "/settings", "setPremiereSettings", "Replace this profile's premiere preferences and broadcaster channel mappings.")
	put.RetrySafety = RetrySafetyNaturalIdempotent
	Register(reg, put, func(ctx context.Context, in *PremiereConfigurationInput) (*PremiereConfigurationOutput, error) {
		if reg.deps.Premieres == nil {
			return nil, unavailable("premieres")
		}
		user, profile, p := viewerIdentity(ctx)
		if p != nil {
			return nil, p
		}
		mappings := make([]notifications.PremiereMapping, 0, len(in.Body.Mappings))
		for _, mapping := range in.Body.Mappings {
			mappings = append(mappings, notifications.PremiereMapping(mapping))
		}
		err := reg.deps.Premieres.SaveConfiguration(ctx, user, profile, notifications.PremierePreferences(in.Body.Preferences), mappings)
		if errors.Is(err, notifications.ErrPremiereInvalid) {
			return nil, NewProblem(TypeValidationFailed, "Choose accessible channels, unique broadcasters and a broadcast window between 15 and 240 minutes.")
		}
		if err != nil {
			return nil, serviceProblem(err)
		}
		cfg, err := reg.deps.Premieres.Configuration(ctx, user, profile)
		if err != nil {
			return nil, serviceProblem(err)
		}
		return &PremiereConfigurationOutput{Body: premiereConfigurationOf(cfg)}, nil
	})
	Register(reg, op(http.MethodGet, "", "listPremieres", "List expected metadata premieres with mapped channels; no EPG assertion is made."), func(ctx context.Context, in *PremiereInput) (*PremiereOutput, error) {
		if reg.deps.Premieres == nil {
			return nil, unavailable("premieres")
		}
		user, profile, p := viewerIdentity(ctx)
		if p != nil {
			return nil, p
		}
		af := handlers.AccessFilterFromContext(ctx, "")
		events, err := reg.deps.Premieres.Events(ctx, user, profile, in.ItemID, af, time.Now())
		if err != nil {
			return nil, serviceProblem(err)
		}
		out := &PremiereOutput{}
		out.Body.Items = make([]Premiere, 0, len(events))
		for _, ev := range events {
			out.Body.Items = append(out.Body.Items, Premiere{EpisodeID: ev.EpisodeID, SeriesID: ev.SeriesID, Title: ev.Title, EpisodeTitle: ev.EpisodeTitle, Season: ev.Season, Episode: ev.Episode, StartsAt: NewInstant(ev.StartsAt), EndsAt: NewInstant(ev.EndsAt), Active: ev.Active, Broadcaster: ev.Broadcaster, Channels: premiereChannelsOf(ev.Channels)})
		}
		return out, nil
	})
}

func premiereChannelsOf(channels []notifications.PremiereChannel) []PremiereChannel {
	out := make([]PremiereChannel, 0, len(channels))
	for _, ch := range channels {
		out = append(out, PremiereChannel(ch))
	}
	return out
}

func premiereConfigurationOf(cfg notifications.PremiereConfiguration) PremiereConfiguration {
	out := PremiereConfiguration{Preferences: PremierePreferences(cfg.Preferences), Mappings: make([]PremiereMapping, 0, len(cfg.Mappings)), Channels: premiereChannelsOf(cfg.Channels), ChannelsAvailable: cfg.ChannelsAvailable}
	for _, m := range cfg.Mappings {
		out.Mappings = append(out.Mappings, PremiereMapping(m))
	}
	return out
}
