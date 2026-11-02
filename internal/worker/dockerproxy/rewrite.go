package dockerproxy

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// mapping moves paths at or under one folder to another.
type mapping struct{ from, to string }

type mappings []mapping

// path moves path by the mapping of the longest folder that holds it, if any.
func (all mappings) path(path string) string {
	longest, moved := -1, path
	for _, m := range all {
		if rest, ok := strings.CutPrefix(path, m.from); ok && (rest == "" || rest[0] == '/') && len(m.from) > longest {
			longest, moved = len(m.from), m.to+rest
		}
	}
	return moved
}

// resolve returns path made clean, as Docker cleans a bind's path before it follows links, with the symbolic links in
// its deepest existing ancestor followed as this process sees them, and the missing rest appended. A symbolic link to
// a missing path can't be followed, so it is an error.
func resolve(path string) (string, error) {
	path = filepath.Clean(path)
	missing := ""
	for dir := path; ; dir = filepath.Dir(dir) {
		resolved, err := filepath.EvalSymlinks(dir)
		if err == nil {
			return filepath.Join(resolved, missing), nil
		}
		if _, lstatErr := os.Lstat(dir); !errors.Is(lstatErr, fs.ErrNotExist) {
			return "", err
		}
		missing = filepath.Join(filepath.Base(dir), missing)
	}
}

// rewriter changes the host paths in a request or an answer with path, and keeps the first error.
type rewriter struct {
	path    func(string) (string, error)
	changed bool
	err     error
}

func (r *rewriter) fail(err error) {
	if r.err == nil {
		r.err = err
	}
}

// key returns the key in object that Docker reads as the struct field name, and whether there is one. Docker's decoder
// ignores the case of field names, so keys that differ only in case make the field ambiguous.
func (r *rewriter) key(object map[string]any, name string) (string, bool) {
	found := ""
	for key := range object {
		if strings.EqualFold(key, name) {
			if found != "" {
				r.fail(fmt.Errorf("%q and %q both set %s", found, key, name))
			}
			found = key
		}
	}
	return found, found != ""
}

func (r *rewriter) field(object map[string]any, name string) any {
	if key, ok := r.key(object, name); ok {
		return object[key]
	}
	return nil
}

func (r *rewriter) mapped(path string) string {
	mapped, err := r.path(path)
	if err != nil {
		r.fail(err)
		return path
	}
	r.changed = r.changed || mapped != path
	return mapped
}

// replace maps the path in object[key], if there is one.
func (r *rewriter) replace(object map[string]any, key string) {
	if path, ok := object[key].(string); ok {
		object[key] = r.mapped(path)
	}
}

// replaceDevice maps the device in the options of a local volume that binds it, where it is a folder. Other devices
// are no folders, such as CIFS's //server/share.
func (r *rewriter) replaceDevice(options map[string]any) {
	o, _ := options["o"].(string)
	for option := range strings.SplitSeq(o, ",") {
		if option == "bind" || option == "rbind" {
			r.replace(options, "device")
			return
		}
	}
}

type rewriteFunc func(object map[string]any, r *rewriter)

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

// rewriteJSON returns body with rewrite applied, or body itself when that changes nothing or body isn't a JSON object,
// and rewrite's error. Decoding into generic values keeps fields that the rewrite doesn't know.
func rewriteJSON(body []byte, rewrite rewriteFunc, path func(string) (string, error)) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	value, err := decode(decoder)
	if errors.As(err, new(repeatedKey)) {
		return nil, err
	}
	object, ok := value.(map[string]any)
	if err != nil || !ok {
		return body, nil
	}
	r := rewriter{path: path}
	rewrite(object, &r)
	if r.err != nil || !r.changed {
		return body, r.err
	}
	var rewritten bytes.Buffer
	encoder := json.NewEncoder(&rewritten)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(object); err != nil {
		return nil, err
	}
	return rewritten.Bytes(), nil
}

type repeatedKey string

func (key repeatedKey) Error() string { return fmt.Sprintf("the JSON repeats %q", string(key)) }

// decode reads a JSON value as the decoder's Decode would into an any, but refuses a repeated key in an object, under
// which Docker merges objects where Decode keeps only the last.
func decode(decoder *json.Decoder) (any, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	switch token {
	case json.Delim('{'):
		object := map[string]any{}
		for decoder.More() {
			token, err := decoder.Token()
			if err != nil {
				return nil, err
			}
			key := token.(string)
			if _, ok := object[key]; ok {
				return nil, repeatedKey(key)
			}
			if object[key], err = decode(decoder); err != nil {
				return nil, err
			}
		}
		_, err = decoder.Token()
		return object, err
	case json.Delim('['):
		array := []any{}
		for decoder.More() {
			value, err := decode(decoder)
			if err != nil {
				return nil, err
			}
			array = append(array, value)
		}
		_, err = decoder.Token()
		return array, err
	}
	return token, nil
}

// rewriteContainer maps the folders of bind mounts and of local volumes bound to a folder, in a container's creation
// or inspection.
func rewriteContainer(container map[string]any, r *rewriter) {
	host, _ := r.field(container, "HostConfig").(map[string]any)
	binds, _ := r.field(host, "Binds").([]any)
	for i, bind := range binds {
		bind, _ := bind.(string)
		// source:target[:options], where a source that isn't absolute names a volume, and a bind without a colon
		// is an anonymous volume at a path in the container.
		if source, rest, ok := strings.Cut(bind, ":"); ok {
			binds[i] = r.mapped(source) + ":" + rest
		}
	}
	// HostConfig.Mounts are the requested mounts, and Mounts are an inspection's actual ones. An absolute Source is a
	// host path whatever the type: volume names and image references can't start with a slash.
	for _, mounts := range []any{r.field(host, "Mounts"), r.field(container, "Mounts")} {
		mounts, _ := mounts.([]any)
		for _, mount := range mounts {
			mount, _ := mount.(map[string]any)
			if key, ok := r.key(mount, "Source"); ok {
				r.replace(mount, key)
			}
			volume, _ := r.field(mount, "VolumeOptions").(map[string]any)
			driver, _ := r.field(volume, "DriverConfig").(map[string]any)
			options, _ := r.field(driver, "Options").(map[string]any)
			r.replaceDevice(options)
		}
	}
}

// rewriteVolume maps the folder of a local volume bound to one, in DriverOpts when creating the volume and in Options
// when inspecting it, and the Mountpoint of an inspection, which is in the user's home with rootless Docker.
func rewriteVolume(volume map[string]any, r *rewriter) {
	options, _ := r.field(volume, "DriverOpts").(map[string]any)
	if options == nil {
		options, _ = r.field(volume, "Options").(map[string]any)
	}
	r.replaceDevice(options)
	if key, ok := r.key(volume, "Mountpoint"); ok {
		r.replace(volume, key)
	}
}
