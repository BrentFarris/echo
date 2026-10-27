package plugins

import (
	"encoding/base64"
	"errors"
	"net/url"
	"regexp"
	"strings"

	"github.com/brent/echo/internal/domain"
)

var appIconHeader = regexp.MustCompile(`(?i)^data:image/(svg\+xml|png|jpeg|webp)(;charset=utf-8)?(;base64)?$`)

func validateApp(m domain.PluginManifest) error {
	if m.App == nil {
		return nil
	}
	if m.App.ViewID == "" {
		return errors.New("app registration needs a floating viewId")
	}
	if _, err := FloatingView(m, m.App.ViewID); err != nil {
		return errors.New("app viewId must name a declared floating view")
	}
	icon := m.App.Icon
	if icon == "" {
		return nil
	}
	if len(icon) > 256<<10 {
		return errors.New("app icon exceeds 256 KiB")
	}
	header, payload, ok := strings.Cut(icon, ",")
	if !ok || !appIconHeader.MatchString(header) || payload == "" {
		return errors.New("app icon must be an SVG, PNG, JPEG, or WebP image data URL")
	}
	var err error
	if strings.HasSuffix(strings.ToLower(header), ";base64") {
		_, err = base64.StdEncoding.DecodeString(payload)
	} else {
		_, err = url.PathUnescape(payload)
	}
	if err != nil {
		return errors.New("app icon has invalid data URL encoding")
	}
	return nil
}

// FloatingView resolves a launch target without treating renderer views as apps.
func FloatingView(m domain.PluginManifest, id string) (domain.View, error) {
	if id == "" && m.App != nil {
		id = m.App.ViewID
	}
	for _, view := range m.Views {
		if (view.Kind == "floating" || view.Kind == "") && (id == "" || view.ID == id) {
			return view, nil
		}
	}
	return domain.View{}, errors.New("floating view not found")
}
