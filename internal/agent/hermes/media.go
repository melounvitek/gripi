package hermes

import (
	"context"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"time"
)

// mediaTag is how Hermes shares a file: MEDIA: and the file's absolute path, which Hermes's messaging platforms and
// desktop app send as an attachment. Models often wrap the tag in Markdown code or emphasis.
var mediaTag = regexp.MustCompile("[`*_]{0,3}MEDIA:[ \t]*(`[^`\n]+`|\"[^\"\n]+\"|\\S+)[`*_]{0,3}")

// codeFence is a line that opens or closes a Markdown code block, with the fence and what follows it.
var codeFence = regexp.MustCompile("^ {0,3}(`{3,}|~{3,})(.*)$")

// mediaImage is an image file that a reply shares, with what read of it.
type mediaImage struct {
	path, mimeType string
	data           []byte
}

// mediaImages takes the tags of the images that the browser can show, which read reads, out of a reply's text. Like
// Hermes, it leaves tags in code blocks and quotes, which are examples, and tags of other files. A reply of nothing but
// tags keeps them as its text, which the page needs to show and announce it as a reply.
func mediaImages(text string, read func(path string) ([]byte, string, error)) (string, []mediaImage) {
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
			data, mimeType, err := read(path)
			if err != nil {
				return tag
			}
			images = append(images, mediaImage{path, mimeType, data})
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

// sniffBytes is as much of a file as its type is told from.
const sniffBytes = 512

// imageTimeout bounds reading a shared image, which the adapter does while it holds the session.
const imageTimeout = 10 * time.Second

// sharedFiles reads at most limit bytes from the start of a regular file that a reply shares, which is on the machine
// that Hermes runs on, and answers them with the file's size. Without it, the adapter reads the gateway's own files.
type sharedFiles func(ctx context.Context, path string, limit int64) ([]byte, int64, error)

func readLocalFile(_ context.Context, path string, limit int64) ([]byte, int64, error) {
	// Opening without blocking keeps a pipe from holding the open until something writes to it.
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return nil, 0, err
	}
	if !info.Mode().IsRegular() {
		return nil, 0, fs.ErrNotExist
	}
	data, err := io.ReadAll(io.LimitReader(file, limit))
	return data, info.Size(), err
}

// image reads at most limit bytes from the start of an image file that the browser can show, and returns them with its
// type.
func (files sharedFiles) image(path string, limit int64) ([]byte, string, error) {
	if !filepath.IsAbs(path) {
		return nil, "", fs.ErrNotExist
	}
	read := files
	if read == nil {
		read = readLocalFile
	}
	ctx, cancel := context.WithTimeout(context.Background(), imageTimeout)
	defer cancel()
	data, size, err := read(ctx, path, limit)
	if err != nil {
		return nil, "", err
	}
	// The file can grow after its size was read.
	if size > sharedImageBytes || len(data) > sharedImageBytes {
		return nil, "", fs.ErrNotExist
	}
	mimeType := http.DetectContentType(data)
	if !slices.Contains([]string{"image/png", "image/jpeg", "image/gif", "image/webp"}, mimeType) {
		return nil, "", fs.ErrNotExist
	}
	return data, mimeType, nil
}

// sniffImage reads as much of an image file that the browser can show as tells its type, and returns it with the type.
func (files sharedFiles) sniffImage(path string) ([]byte, string, error) {
	return files.image(path, sniffBytes)
}

// readImage reads an image file that the browser can show, and returns it with its type.
func (files sharedFiles) readImage(path string) ([]byte, string, error) {
	return files.image(path, sharedImageBytes+1)
}
