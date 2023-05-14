package core

import (
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"sync"

	"github.com/reeflective/readline/inputrc"
)

const (
	keyScanBufSize = 1024
)

var rxRcvCursorPos = regexp.MustCompile(`\x1b\[([0-9]+);([0-9]+)R`)

// Keys is used read, manage and use keys input by the shell user.
type Keys struct {
	buf        []byte       // Keys read and waiting to be used.
	matched    []rune       // Keys that have been successfully matched against a bind.
	matchedLen int          // Number of keys matched against commands.
	macroKeys  []rune       // Keys that have been fed by a macro.
	mustWait   bool         // Keys are in the stack, but we must still read stdin.
	waiting    bool         // Currently waiting for keys on stdin.
	reading    bool         // Currently reading keys out of the main loop.
	keysOnce   chan []byte  // Passing keys from the main routine.
	cursor     chan []byte  // Cursor coordinates has been read on stdin.
	mutex      sync.RWMutex // Concurrency safety
}

// WaitAvailableKeys waits until an input key is either read from standard input,
// or directly returns if the key stack still/already has available keys.
func WaitAvailableKeys(keys *Keys) {
	if len(keys.buf) > 0 && !keys.mustWait {
		return
	}

	// The macro engine might have fed some keys
	if len(keys.macroKeys) > 0 {
		return
	}

	keys.mutex.Lock()
	keys.waiting = true
	keys.mutex.Unlock()

	defer func() {
		keys.mutex.Lock()
		keys.waiting = false
		keys.mutex.Unlock()
	}()

	for {
		// Start reading from os.Stdin in the background.
		// We will either read keyBuf from user, or an EOF
		// send by ourselves, because we pause reading.
		keyBuf, err := keys.readInputFiltered()
		if err != nil && errors.Is(err, io.EOF) {
			return
		}

		if len(keyBuf) == 0 {
			continue
		}

		switch {
		case keys.reading:
			keys.keysOnce <- keyBuf
			continue

		default:
			keys.mutex.RLock()
			keys.buf = append(keys.buf, keyBuf...)
			keys.mutex.RUnlock()
		}

		return
	}
}

// PopKey is used to pop a key off the key stack without
// yet marking this key as having matched a command.
func PopKey(keys *Keys) (key byte, empty bool) {
	switch {
	case len(keys.buf) > 0:
		key = keys.buf[0]
		keys.buf = keys.buf[1:]
	case len(keys.macroKeys) > 0:
		key = byte(keys.macroKeys[0])
		keys.macroKeys = keys.macroKeys[1:]
	default:
		return byte(0), true
	}

	return key, false
}

// PeekKey returns the first key in the stack, without removing it.
func PeekKey(keys *Keys) (key byte, empty bool) {
	switch {
	case len(keys.buf) > 0:
		key = keys.buf[0]
	case len(keys.macroKeys) > 0:
		key = byte(keys.macroKeys[0])
	default:
		return byte(0), true
	}

	return key, false
}

// MatchedKeys is used to indicate how many keys have been evaluated against the shell
// commands in the dispatching process (regardless of if a command was matched or not).
// This function should normally not be used by external users of the library.
func MatchedKeys(keys *Keys, matched []byte, args ...byte) {
	if len(matched) > 0 {
		keys.matched = append(keys.matched, []rune(string(matched))...)
		keys.matchedLen += len(matched)
	}

	if len(args) > 0 {
		keys.buf = append(args, keys.buf...)
	}
}

// MatchedPrefix is similar to MatchedKeys, except that the provided keys
// should not be flushed, since they only matched some binds by prefix and
// that we need more keys for an exact match (or failure).
func MatchedPrefix(keys *Keys, prefix ...byte) {
	if len(prefix) == 0 {
		return
	}

	keys.mutex.Lock()
	defer keys.mutex.Unlock()

	// Our keys are still considered unread, but they have been:
	// if there is no more keys in the stack, the next blocking
	// call to WaitAvailableKeys() should block for new keys.
	keys.mustWait = len(keys.buf) == 0
	keys.buf = append(prefix, keys.buf...)

	keys.matched = []rune(string(prefix))
	keys.matchedLen = len(prefix)
}

// FlushUsed drops the keys that have matched a given command.
func FlushUsed(keys *Keys) {
	keys.mutex.Lock()
	defer keys.mutex.Unlock()

	defer func() {
		keys.matchedLen = 0
		keys.matched = nil
	}()

	if keys.matchedLen == 0 {
		return
	}

	keys.matched = nil
}

// ReadKey reads keys from stdin like Read(), but immediately
// returns them instead of storing them in the stack, along with
// an indication on whether this key is an escape/abort one.
func (k *Keys) ReadKey() (key rune, isAbort bool) {
	k.mutex.RLock()
	k.keysOnce = make(chan []byte)
	k.reading = true
	k.mutex.RUnlock()

	defer func() {
		k.mutex.RLock()
		k.reading = false
		k.mutex.RUnlock()
	}()

	switch {
	case len(k.macroKeys) > 0:
		key = k.macroKeys[0]
		k.macroKeys = k.macroKeys[1:]

	case k.waiting:
		buf := <-k.keysOnce
		key = []rune(string(buf))[0]
	default:
		buf, _ := k.readInputFiltered()
		key = []rune(string(buf))[0]
	}

	// Always mark those keys as matched, so that
	// if the macro engine is recording, it will
	// capture them
	k.matched = append(k.matched, key)
	k.matchedLen++

	return key, key == inputrc.Esc
}

// Pop removes the first byte in the key stack (first read) and returns it.
// It returns either a key and the empty boolean set to false, or if no keys
// are present, returns a zero rune and empty set to true.
// The key bytes returned by this function are not those that have been
// matched against the current command. The keys returned here are only
// keys that have not yet been dispatched. (ex: di" will match vim delete-to,
// then select-inside, but the quote won't match a command and will be passed
// to select-inside. This function Pop() will thus return the quote.)
func (k *Keys) Pop() (key byte, empty bool) {
	switch {
	case len(k.buf) > 0:
		key = k.buf[0]
		k.buf = k.buf[1:]
	case len(k.macroKeys) > 0:
		key = byte(k.macroKeys[0])
		k.macroKeys = k.macroKeys[1:]
	default:
		return byte(0), true
	}

	k.matched = append(k.matched, rune(key))
	k.matchedLen++

	return key, false
}

// Caller returns the keys that have matched the command currently being ran.
func (k *Keys) Caller() (keys []rune) {
	return k.matched
}

// Feed can be used to directly add keys to the stack.
// If begin is true, the keys are added on the top of
// the stack, otherwise they are being appended to it.
func (k *Keys) Feed(begin bool, keys ...rune) {
	if len(keys) == 0 {
		return
	}

	keyBuf := []rune(string(keys))

	k.mutex.Lock()
	defer k.mutex.Unlock()

	if begin {
		k.macroKeys = append(keyBuf, k.macroKeys...)
		// k.buf = append(keyBuf, k.buf...)
	} else {
		k.macroKeys = append(k.macroKeys, keyBuf...)
		// k.buf = append(k.buf, keyBuf...)
	}
}

// GetCursorPos returns the current cursor position in the terminal.
// It is safe to call this function even if the shell is reading input.
func (k *Keys) GetCursorPos() (x, y int) {
	k.cursor = make(chan []byte)

	disable := func() (int, int) {
		os.Stderr.WriteString("\r\ngetCursorPos() not supported by terminal emulator, disabling....\r\n")
		return -1, -1
	}

	var cursor []byte
	var match [][]string

	// Echo the query and wait for the main key
	// reading routine to send us the response back.
	fmt.Print("\x1b[6n")

	// In order not to get stuck with an input that might be user-one
	// (like when the user typed before the shell is fully started, and yet not having
	// queried cursor yet), we keep reading from stdin until we find the cursor response.
	// Everything else is passed back as user input.
	for {
		switch {
		case k.waiting, k.reading:
			cursor = <-k.cursor
		default:
			buf := make([]byte, keyScanBufSize)

			read, err := os.Stdin.Read(buf)
			if err != nil {
				return disable()
			}

			cursor = buf[:read]
		}

		// We have read (or have been passed) something.
		if len(cursor) == 0 {
			return disable()
		}

		// Attempt to locate cursor response in it.
		match = rxRcvCursorPos.FindAllStringSubmatch(string(cursor), 1)

		// If there is something but not cursor answer, its user input.
		if len(match) == 0 && len(cursor) > 0 {
			k.mutex.RLock()
			k.buf = append(k.buf, cursor...)
			k.mutex.RUnlock()

			continue
		}

		// And if empty, then we should abort.
		if len(match) == 0 {
			return disable()
		}

		break
	}

	// We know that we have a cursor answer, process it.
	y, err := strconv.Atoi(match[0][1])
	if err != nil {
		return disable()
	}

	x, err = strconv.Atoi(match[0][2])
	if err != nil {
		return disable()
	}

	return x, y
}

func (k *Keys) extractCursorPos(keys []byte) (cursor, remain []byte) {
	if !rxRcvCursorPos.Match(keys) {
		return cursor, keys
	}

	allCursors := rxRcvCursorPos.FindAll(keys, -1)
	cursor = allCursors[len(allCursors)-1]
	remain = rxRcvCursorPos.ReplaceAll(keys, nil)

	return
}

func (k *Keys) readInputFiltered() (keys []byte, err error) {
	// Start reading from os.Stdin in the background.
	// We will either read keys from user, or an EOF
	// send by ourselves, because we pause reading.
	buf := make([]byte, keyScanBufSize)

	read, err := os.Stdin.Read(buf)
	if err != nil && errors.Is(err, io.EOF) {
		return
	}

	// Always attempt to extract cursor position info.
	// If found, strip it and keep the remaining keys.
	cursor, keys := k.extractCursorPos(buf[:read])

	if len(cursor) > 0 {
		k.cursor <- cursor
	}

	return keys, nil
}