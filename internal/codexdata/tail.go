package codexdata

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"sync"
)

const (
	maxPooledTailBytes = 2 * 1024 * 1024
	contextReadChunk   = 64 * 1024
)

var tailBufferPool sync.Pool

type contextFileReader struct {
	ctx     context.Context
	file    *os.File
	metrics *ReadMetrics
}

type contextChunkReader struct {
	ctx       context.Context
	reader    io.Reader
	metrics   *ReadMetrics
	kind      sourceReadKind
	readError error
}

func (reader *contextChunkReader) Read(buffer []byte) (int, error) {
	if err := reader.ctx.Err(); err != nil {
		reader.readError = err
		return 0, err
	}
	if err := reader.metrics.beforeSourceRead(reader.kind); err != nil {
		reader.readError = err
		return 0, err
	}
	if len(buffer) > contextReadChunk {
		buffer = buffer[:contextReadChunk]
	}
	count, err := reader.reader.Read(buffer)
	if err != nil && !errors.Is(err, io.EOF) {
		reader.readError = err
	}
	return count, err
}

func readTail(path string, maxBytes int64) (string, error) {
	return readTailContext(context.Background(), path, maxBytes, nil)
}

func readTailContext(
	ctx context.Context,
	path string,
	maxBytes int64,
	metrics *ReadMetrics,
) (string, error) {
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
	reader := contextFileReader{ctx: ctx, file: file, metrics: metrics}
	data, err := reader.readRange(info.Size()-bytesToRead, bytesToRead)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func visitTailBytes(path string, maxBytes int64, visit func([]byte)) error {
	return visitTailBytesContext(
		context.Background(),
		path,
		maxBytes,
		nil,
		visit,
	)
}

func visitTailBytesContext(
	ctx context.Context,
	path string,
	maxBytes int64,
	metrics *ReadMetrics,
	visit func([]byte),
) error {
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
	buffer, _ := tailBufferPool.Get().([]byte)
	if int64(cap(buffer)) < bytesToRead {
		buffer = make([]byte, bytesToRead)
	} else {
		buffer = buffer[:bytesToRead]
	}
	reader := contextFileReader{ctx: ctx, file: file, metrics: metrics}
	read, err := reader.readInto(info.Size()-bytesToRead, buffer)
	if err != nil {
		return err
	}
	visit(buffer[:read])
	if cap(buffer) <= maxPooledTailBytes {
		tailBufferPool.Put(buffer[:0])
	}
	return nil
}

func (reader contextFileReader) readRange(
	offset int64,
	length int64,
) ([]byte, error) {
	if length <= 0 {
		return []byte{}, reader.ctx.Err()
	}
	data := make([]byte, length)
	read, err := reader.readInto(offset, data)
	return data[:read], err
}

func (reader contextFileReader) readInto(
	offset int64,
	buffer []byte,
) (int, error) {
	if err := reader.ctx.Err(); err != nil {
		return 0, err
	}
	if _, err := reader.file.Seek(offset, io.SeekStart); err != nil {
		return 0, err
	}
	total := 0
	for total < len(buffer) {
		if err := reader.ctx.Err(); err != nil {
			return total, err
		}
		end := min(len(buffer), total+contextReadChunk)
		read, err := reader.file.Read(buffer[total:end])
		if read > 0 {
			total += read
			if hookErr := reader.metrics.addTailBytes(read); hookErr != nil {
				return total, hookErr
			}
		}
		switch {
		case err == nil:
			if read == 0 {
				return total, io.ErrNoProgress
			}
		case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
			return total, nil
		default:
			return total, err
		}
	}
	return total, nil
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
