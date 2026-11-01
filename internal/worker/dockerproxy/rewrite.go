package dockerproxy

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
)

// mapping moves paths at or under one folder to another.
type mapping struct{ from, to string }

func (m mapping) path(path string) (string, bool) {
	if rest, ok := strings.CutPrefix(path, m.from); ok && (rest == "" || rest[0] == '/') {
		return m.to + rest, true
	}
	return path, false
}

// replace maps the path in object[key], if there is one.
func (m mapping) replace(object map[string]any, key string) bool {
	path, _ := object[key].(string)
	mapped, ok := m.path(path)
	if ok {
		object[key] = mapped
	}
	return ok
}

type rewriteFunc func(object map[string]any, m mapping) (changed bool)

func requestRewrite(r *http.Request) rewriteFunc {
	if r.Method != http.MethodPost {
		return nil
	}
	switch apiPath(r.URL.Path) {
	case "/containers/create":
		return rewriteContainer
	case "/volumes/create":
		return rewriteVolume
	}
	return nil
}

// responseRewrite maps paths back in inspections, so that the docker CLI shows the paths it was given.
func responseRewrite(r *http.Request) rewriteFunc {
	if r.Method != http.MethodGet {
		return nil
	}
	parts := strings.Split(apiPath(r.URL.Path), "/")
	switch {
	case len(parts) == 4 && parts[1] == "containers" && parts[3] == "json":
		return rewriteContainer
	case len(parts) == 3 && parts[1] == "volumes":
		return rewriteVolume
	}
	return nil
}

// apiPath removes the API version, such as /v1.52, from a request path.
func apiPath(path string) string {
	if rest, ok := strings.CutPrefix(path, "/v"); ok {
		if version, _, ok := strings.Cut(rest, "/"); ok && version != "" && strings.Trim(version, "0123456789.") == "" {
			return rest[len(version):]
		}
	}
	return path
}

// rewriteJSON returns body with rewrite applied, or body itself when that changes nothing or body isn't a JSON object.
// Decoding into generic values keeps fields that the rewrite doesn't know.
func rewriteJSON(body []byte, rewrite rewriteFunc, m mapping) []byte {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var object map[string]any
	if decoder.Decode(&object) != nil || !rewrite(object, m) {
		return body
	}
	var rewritten bytes.Buffer
	encoder := json.NewEncoder(&rewritten)
	encoder.SetEscapeHTML(false)
	if encoder.Encode(object) != nil {
		return body
	}
	return rewritten.Bytes()
}

// rewriteContainer maps the folders of bind mounts and of local volumes bound to a folder, in a container's creation
// or inspection.
func rewriteContainer(container map[string]any, m mapping) bool {
	changed := false
	host, _ := container["HostConfig"].(map[string]any)
	binds, _ := host["Binds"].([]any)
	for i, bind := range binds {
		bind, _ := bind.(string)
		// source:target[:options], where a source that isn't absolute names a volume, and a bind without a colon
		// is an anonymous volume at a path in the container.
		if source, rest, ok := strings.Cut(bind, ":"); ok {
			if mapped, ok := m.path(source); ok {
				binds[i] = mapped + ":" + rest
				changed = true
			}
		}
	}
	// HostConfig.Mounts are the requested mounts, and Mounts are an inspection's actual ones. A Source under the
	// folder is a host path whatever the type: volume names and image references can't start with a slash.
	for _, mounts := range []any{host["Mounts"], container["Mounts"]} {
		mounts, _ := mounts.([]any)
		for _, mount := range mounts {
			mount, _ := mount.(map[string]any)
			volume, _ := mount["VolumeOptions"].(map[string]any)
			driver, _ := volume["DriverConfig"].(map[string]any)
			options, _ := driver["Options"].(map[string]any)
			source := m.replace(mount, "Source")
			device := m.replace(options, "device")
			changed = changed || source || device
		}
	}
	return changed
}

// rewriteVolume maps the folder of a local volume bound to one, in DriverOpts when creating the volume and in Options
// when inspecting it, and the Mountpoint of an inspection, which is in the user's home with rootless Docker.
func rewriteVolume(volume map[string]any, m mapping) bool {
	options, _ := volume["DriverOpts"].(map[string]any)
	if options == nil {
		options, _ = volume["Options"].(map[string]any)
	}
	device := m.replace(options, "device")
	mountpoint := m.replace(volume, "Mountpoint")
	return device || mountpoint
}
