package codexdata

import (
	"errors"
	"io"
	"os"
)

func readTail(path string, maxBytes int64) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return "", err
	}
	if info.Size() <= 0 || maxBytes <= 0 {
		return "", nil
	}

	bytesToRead := min(info.Size(), maxBytes)
	if _, err := file.Seek(-bytesToRead, io.SeekEnd); err != nil {
		return "", err
	}
	data := make([]byte, bytesToRead)
	read, err := io.ReadFull(file, data)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
		return "", err
	}
	return string(data[:read]), nil
}
