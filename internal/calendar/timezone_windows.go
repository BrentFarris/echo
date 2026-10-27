//go:build windows

package calendar

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"golang.org/x/sys/windows/registry"
)

// Mapping from Unicode CLDR windowsZones.xml, territory 001; see UNICODE-LICENSE.txt.
//
//go:embed windowszones.json
var windowsZones []byte

func LocalTimezone() (string, error) {
	key, e := registry.OpenKey(registry.LOCAL_MACHINE, `SYSTEM\CurrentControlSet\Control\TimeZoneInformation`, registry.QUERY_VALUE)
	if e != nil {
		return "", e
	}
	defer key.Close()
	name, _, e := key.GetStringValue("TimeZoneKeyName")
	if e != nil {
		return "", e
	}
	var zones map[string]string
	if e = json.Unmarshal(windowsZones, &zones); e != nil {
		return "", e
	}
	if zone := zones[name]; zone != "" {
		return zone, nil
	}
	return "", fmt.Errorf("cannot resolve Windows timezone %q; select an IANA timezone in Calendar settings", name)
}
