package denoise

import "regexp"

var (
	hashRe         = regexp.MustCompile(`[A-Za-z0-9]{64,}`)
	uuidRe         = regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)
	pointerRe      = regexp.MustCompile(`0x[0-9a-f]+`)
	numberRe       = regexp.MustCompile(`[0-9]+`)
	randomSuffixRe = regexp.MustCompile(`([._-])[A-Za-z0-9._-]+`)
	spaceRe        = regexp.MustCompile(`[ \t]+`)
)

func Denoise(line string) string {
	line = hashRe.ReplaceAllString(line, "HASH")
	line = uuidRe.ReplaceAllString(line, "UUID")
	line = pointerRe.ReplaceAllString(line, "0xdeadbeef")
	line = numberRe.ReplaceAllString(line, "0")
	line = randomSuffixRe.ReplaceAllString(line, "${1}RANDOM")
	line = spaceRe.ReplaceAllString(line, " ")
	return line
}
