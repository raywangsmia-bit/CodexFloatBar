package codexdata

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
)

// sessionTailState retains parsed, renderer-safe values and parsing offsets only.
// Raw session lines live only for the duration of one update call.
type sessionTailState struct {
	file               sessionFile
	safeOffset         int64
	skipToNewline      bool
	unterminatedParsed bool
	turn               RuntimeStatus
	turnFound          bool
	speedTier          string
	rate               *rateLimitCandidate
}

type sessionSignals struct {
	runtime      RuntimeStatus
	runtimeFound bool
	rate         *rateLimitCandidate
}

func (service *Service) readSessionSignals(
	ctx context.Context,
	files []sessionFile,
) (sessionSignals, error) {
	service.tailMu.Lock()
	defer service.tailMu.Unlock()

	files = limitSessionFiles(files, 16)
	next := make(map[string]sessionTailState, len(files))
	states := make([]sessionTailState, 0, len(files))
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return sessionSignals{}, err
		}
		key := cachePathKey(file.path)
		state, err := updateSessionTailState(
			ctx,
			service.sessionTails[key],
			file,
			service.metrics,
		)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return sessionSignals{}, ctxErr
			}
			return sessionSignals{}, err
		}
		next[key] = state
		states = append(states, state)
	}

	result := sessionSignals{}
	for index, state := range states {
		if index < 4 && !result.runtimeFound {
			result.runtime, result.runtimeFound = state.runtimeStatus()
		}
		if state.rate == nil {
			continue
		}
		candidate := cloneRateLimitCandidate(*state.rate)
		candidate.pathIndex = index
		if newerRateLimitCandidate(candidate, result.rate) {
			result.rate = &candidate
		}
	}
	service.sessionTails = next
	return result, nil
}

func updateSessionTailState(
	ctx context.Context,
	previous sessionTailState,
	current sessionFile,
	metrics *ReadMetrics,
) (sessionTailState, error) {
	if sameSessionFile(previous.file, current) {
		return previous, nil
	}

	hasPrevious := previous.file.path != ""
	samePath := cachePathKey(previous.file.path) == cachePathKey(current.path)
	sameIdentity := sameFileIdentity(previous.file.info, current.info)
	grewFromSafeOffset := current.size > previous.file.size &&
		current.size >= previous.safeOffset
	appendable := hasPrevious && samePath && sameIdentity && grewFromSafeOffset
	state := previous
	readOffset := previous.safeOffset
	if !appendable {
		state = sessionTailState{file: current}
		readOffset = max(int64(0), current.size-sessionTailBytes)
		state.safeOffset = readOffset
		state.skipToNewline = readOffset > 0
	}

	file, err := os.Open(current.path)
	if err != nil {
		return sessionTailState{}, err
	}
	defer file.Close()
	reader := contextFileReader{ctx: ctx, file: file, metrics: metrics}

	if appendable && state.unterminatedParsed {
		appended, err := reader.readRange(
			previous.file.size,
			current.size-previous.file.size,
		)
		if err != nil {
			return sessionTailState{}, err
		}
		if int64(len(appended)) != current.size-previous.file.size {
			return sessionTailState{}, io.ErrUnexpectedEOF
		}
		if len(appended) > 0 && appended[0] == '\n' {
			state.unterminatedParsed = false
			state.safeOffset = previous.file.size + 1
			parseSessionTailBytes(&state, appended[1:], state.safeOffset)
			if err := ctx.Err(); err != nil {
				return sessionTailState{}, err
			}
			state.file = current
			return state, nil
		}
		state = sessionTailState{file: current}
		readOffset = max(int64(0), current.size-sessionTailBytes)
		state.safeOffset = readOffset
		state.skipToNewline = readOffset > 0
	}

	tailExceeded := current.size-readOffset > sessionTailBytes
	previousPartial := state.safeOffset < previous.file.size
	if tailExceeded && previousPartial {
		readOffset = current.size - sessionTailBytes
		state.safeOffset = readOffset
		state.skipToNewline = true
		state.unterminatedParsed = false
	}
	length := current.size - readOffset
	data, err := reader.readRange(readOffset, length)
	if err != nil {
		return sessionTailState{}, err
	}
	if int64(len(data)) != length {
		return sessionTailState{}, io.ErrUnexpectedEOF
	}
	parseSessionTailBytes(&state, data, readOffset)
	if err := ctx.Err(); err != nil {
		return sessionTailState{}, err
	}
	state.file = current
	return state, nil
}

func parseSessionTailBytes(state *sessionTailState, data []byte, baseOffset int64) {
	cursor := 0
	if state.skipToNewline {
		newline := bytes.IndexByte(data, '\n')
		if newline < 0 {
			state.safeOffset = baseOffset + int64(len(data))
			return
		}
		cursor = newline + 1
		state.safeOffset = baseOffset + int64(cursor)
		state.skipToNewline = false
	}

	for cursor < len(data) {
		newline := bytes.IndexByte(data[cursor:], '\n')
		if newline < 0 {
			break
		}
		lineEnd := cursor + newline
		line := bytes.TrimSuffix(data[cursor:lineEnd], []byte{'\r'})
		state.consumeLine(line, baseOffset+int64(cursor))
		cursor = lineEnd + 1
		state.safeOffset = baseOffset + int64(cursor)
		state.unterminatedParsed = false
	}

	if cursor >= len(data) {
		state.safeOffset = baseOffset + int64(len(data))
		return
	}
	remainder := data[cursor:]
	trimmed := bytes.TrimSpace(remainder)
	if len(trimmed) > 0 && json.Valid(trimmed) {
		state.consumeLine(trimmed, baseOffset+int64(cursor))
		state.safeOffset = baseOffset + int64(len(data))
		state.unterminatedParsed = true
		return
	}
	state.safeOffset = baseOffset + int64(cursor)
}

func (state *sessionTailState) consumeLine(line []byte, offset int64) {
	if len(bytes.TrimSpace(line)) == 0 {
		return
	}
	if bytes.Contains(line, []byte("turn_context")) {
		if status, ok := parseTurnContext(line); ok {
			state.turn = status
			state.turnFound = true
		}
	}
	if tier := findLatestSpeedTierBytes(line); tier != "" {
		state.speedTier = tier
	}
	if bytes.Contains(line, []byte(`"rate_limits"`)) {
		if candidate := parseRateLimitCandidate(line, 0, int(offset)); candidate != nil {
			state.rate = candidate
		}
	}
}

func (state sessionTailState) runtimeStatus() (RuntimeStatus, bool) {
	status := state.turn
	if status.SpeedTier == "" {
		status.SpeedTier = state.speedTier
	}
	if state.turnFound {
		return status, true
	}
	return buildRuntimeStatus("", "", state.speedTier)
}

func cloneRateLimitCandidate(candidate rateLimitCandidate) rateLimitCandidate {
	candidate.primary = cloneRateLimitWindow(candidate.primary)
	candidate.secondary = cloneRateLimitWindow(candidate.secondary)
	if candidate.observedAt != nil {
		observedAt := *candidate.observedAt
		candidate.observedAt = &observedAt
	}
	return candidate
}
