package game

import "strings"

// MaxTranscriptBytes bounds the stored transcript.
//
// A Zork turn is a few hundred bytes, so this is a few hundred turns of
// scrollback — far more than a player reads back through, and short of a row
// that grows without limit. The oldest lines go first.
const MaxTranscriptBytes = 64 * 1024

// appendTranscript adds one exchange to the transcript.
//
// The story's output already ends with its prompt and no newline after it, so
// the player's line continues that same line exactly as the terminal showed it,
// and the story's answer follows on the next. Nothing is inserted between them.
func appendTranscript(transcript, command, output string) string {
	var b strings.Builder
	b.Grow(len(transcript) + len(command) + len(output) + 1)

	b.WriteString(transcript)
	b.WriteString(command)
	b.WriteString("\n")
	b.WriteString(output)

	return trimTranscript(b.String(), MaxTranscriptBytes)
}

// trimTranscript drops the oldest lines until the transcript fits.
//
// It cuts at a line boundary so that what remains still reads as a terminal
// rather than starting mid-word.
func trimTranscript(transcript string, limit int) string {
	if len(transcript) <= limit {
		return transcript
	}

	cut := transcript[len(transcript)-limit:]
	if i := strings.IndexByte(cut, '\n'); i >= 0 {
		return cut[i+1:]
	}

	// One line longer than the whole limit: keep its tail rather than nothing.
	return cut
}
