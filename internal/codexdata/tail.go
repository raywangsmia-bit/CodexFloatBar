package codexdata

import (
	"bytes"
	"errors"
	"io"
	"os"
	"sync"
)

const maxPooledTailBytes = 2 * 1024 * 1024

var tailBufferPool sync.Pool

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

func visitTailBytes(path string, maxBytes int64, visit func([]byte)) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return err
	}
	if info.Size() <= 0 || maxBytes <= 0 {
		visit(nil)
		return nil
	}

	bytesToRead := min(info.Size(), maxBytes)
	if _, err := file.Seek(-bytesToRead, io.SeekEnd); err != nil {
		return err
	}
	buffer, _ := tailBufferPool.Get().([]byte)
	if int64(cap(buffer)) < bytesToRead {
		buffer = make([]byte, bytesToRead)
	} else {
		buffer = buffer[:bytesToRead]
	}
	read, err := io.ReadFull(file, buffer)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
		return err
	}
	visit(buffer[:read])
	if cap(buffer) <= maxPooledTailBytes {
		tailBufferPool.Put(buffer[:0])
	}
	return nil
}

func visitLinesReverseBytes(text []byte, visit func(line []byte, offset int) bool) {
	end := len(text)
	for end > 0 {
		if text[end-1] == '\n' {
			end--
			continue
		}
		start := bytes.LastIndexByte(text[:end], '\n') + 1
		line := text[start:end]
		if len(line) > 0 && line[len(line)-1] == '\r' {
			line = line[:len(line)-1]
		}
		if !visit(line, start) || start == 0 {
			return
		}
		end = start - 1
	}
}
