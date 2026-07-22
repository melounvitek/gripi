package state

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

const (
	fileMode      = 0o600
	directoryMode = 0o700
)

type File struct {
	path string
}

func NewFile(path string) *File {
	return &File{path: path}
}

func (f *File) Read() ([]byte, bool, error) {
	if err := f.secureDefaultDirectory(); err != nil {
		return nil, false, err
	}
	file, err := os.Open(f.path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	defer file.Close()
	if err := file.Chmod(fileMode); err != nil {
		return nil, false, err
	}
	contents, err := io.ReadAll(file)
	if err != nil {
		return nil, false, err
	}
	return contents, true, nil
}

func (f *File) Write(contents []byte) error {
	temporaryPath, err := f.prepare(contents)
	if err != nil {
		return err
	}
	defer os.Remove(temporaryPath)
	return os.Rename(temporaryPath, f.path)
}

func (f *File) CreateOnce(contents []byte) (bool, error) {
	temporaryPath, err := f.prepare(contents)
	if err != nil {
		return false, err
	}
	defer os.Remove(temporaryPath)
	if err := os.Link(temporaryPath, f.path); errors.Is(err, fs.ErrExist) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	return true, nil
}

func (f *File) prepare(contents []byte) (string, error) {
	directory := filepath.Dir(f.path)
	mode := fs.FileMode(0o777)
	if f.isDefaultDirectory(directory) {
		mode = directoryMode
	}
	if err := os.MkdirAll(directory, mode); err != nil {
		return "", err
	}
	if mode == directoryMode {
		if err := os.Chmod(directory, directoryMode); err != nil {
			return "", err
		}
	}

	file, err := os.CreateTemp(directory, "."+filepath.Base(f.path)+"-*.tmp")
	if err != nil {
		return "", err
	}
	path := file.Name()
	remove := true
	defer func() {
		file.Close()
		if remove {
			os.Remove(path)
		}
	}()
	if err := file.Chmod(fileMode); err != nil {
		return "", err
	}
	if _, err := file.Write(contents); err != nil {
		return "", err
	}
	if err := file.Sync(); err != nil {
		return "", err
	}
	if err := file.Close(); err != nil {
		return "", err
	}
	remove = false
	return path, nil
}

func (f *File) secureDefaultDirectory() error {
	directory := filepath.Dir(f.path)
	if !f.isDefaultDirectory(directory) {
		return nil
	}
	if _, err := os.Stat(directory); errors.Is(err, fs.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	return os.Chmod(directory, directoryMode)
}

func (f *File) isDefaultDirectory(directory string) bool {
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	actual, err := filepath.Abs(directory)
	if err != nil {
		return false
	}
	expected, err := filepath.Abs(filepath.Join(home, ".pi", "gripi"))
	return err == nil && actual == expected
}
