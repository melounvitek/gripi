package hermes

import (
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
)

// mediaTag is how Hermes shares a file: MEDIA: and the file's absolute path, which Hermes's messaging platforms and
// desktop app send as an attachment. Models often wrap the tag in Markdown code or emphasis.
var mediaTag = regexp.MustCompile("[`*_]{0,3}MEDIA:[ \t]*(`[^`\n]+`|\"[^\"\n]+\"|\\S+)[`*_]{0,3}")

// codeFence is a line that opens or closes a Markdown code block, with the fence and what follows it.
var codeFence = regexp.MustCompile("^ {0,3}(`{3,}|~{3,})(.*)$")

// mediaImage is an image file that a reply shares.
type mediaImage struct{ path, mimeType string }

// mediaImages takes the tags of the images that the browser can show out of a reply's text. Like Hermes, it leaves
// tags in code blocks and quotes, which are examples, and tags of other files. A reply of nothing but tags keeps them
// as its text, which the page needs to show and announce it as a reply.
func mediaImages(text string) (string, []mediaImage) {
	var images []mediaImage
	lines := strings.SplitAfter(text, "\n")
	// fence opened the code block that the line is in. Only a fence of its character, at least as long, closes it.
	fence := ""
	for index, line := range lines {
		if match := codeFence.FindStringSubmatch(strings.TrimRight(line, "\r\n")); match != nil {
			// A line of backticks with another one after them is inline code, not a fence.
			if fence == "" && !(match[1][0] == '`' && strings.Contains(match[2], "`")) {
				fence = match[1]
				continue
			}
			if fence != "" && match[1][0] == fence[0] && len(match[1]) >= len(fence) && strings.TrimSpace(match[2]) == "" {
				fence = ""
				continue
			}
		}
		if fence != "" || strings.HasPrefix(strings.TrimSpace(line), ">") {
			continue
		}
		lines[index] = mediaTag.ReplaceAllStringFunc(line, func(tag string) string {
			path := strings.TrimRight(strings.Trim(mediaTag.FindStringSubmatch(tag)[1], "`\""), "`*_.,;:!?)")
			file, mimeType, err := openImage(path)
			if err != nil {
				return tag
			}
			_ = file.Close()
			images = append(images, mediaImage{path, mimeType})
			return ""
		})
	}
	if shown := strings.Join(lines, ""); strings.TrimSpace(shown) != "" {
		return shown, images
	}
	return text, images
}

// sharedImageBytes is the largest image that Gripi shows from a reply, as large as an image it attaches to a prompt.
const sharedImageBytes = 10 << 20

// openImage opens an image file that the browser can show, and returns it with its type.
func openImage(path string) (*os.File, string, error) {
	if !filepath.IsAbs(path) {
		return nil, "", fs.ErrNotExist
	}
	// Opening without blocking keeps a pipe from holding the open until something writes to it.
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, "", err
	}
	if info, err := file.Stat(); err == nil && info.Mode().IsRegular() && info.Size() <= sharedImageBytes {
		head := make([]byte, 512)
		count, _ := file.ReadAt(head, 0)
		mimeType := http.DetectContentType(head[:count])
		if slices.Contains([]string{"image/png", "image/jpeg", "image/gif", "image/webp"}, mimeType) {
			return file, mimeType, nil
		}
	}
	_ = file.Close()
	return nil, "", fs.ErrNotExist
}

func readImage(path string) ([]byte, string, error) {
	file, mimeType, err := openImage(path)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = file.Close() }()
	// The file can grow after it was checked.
	data, err := io.ReadAll(io.LimitReader(file, sharedImageBytes+1))
	if err == nil && len(data) > sharedImageBytes {
		err = fs.ErrNotExist
	}
	return data, mimeType, err
}
