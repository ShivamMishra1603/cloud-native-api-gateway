package integration

import "bytes"

// bytesContain returns true if sub is found in data.
func bytesContain(data []byte, sub string) bool {
	return bytes.Contains(data, []byte(sub))
}
