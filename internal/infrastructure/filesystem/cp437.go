package filesystem

import "strings"

// IBM code page 437: the encoding of zip entry names that do not carry the UTF-8 flag.
// 0x01–0x1F and 0x7F are the code page's graphic characters, not control codes: tools that
// write CP437 store "§" (Minecraft's formatting prefix, common in world names) as 0x15.
var (
	cp437Low  = []rune("☺☻♥♦♣♠•◘○◙♂♀♪♫☼►◄↕‼¶§▬↨↑↓→←∟↔▲▼")
	cp437High = []rune("ÇüéâäàåçêëèïîìÄÅÉæÆôöòûùÿÖÜ¢£¥₧ƒ" +
		"áíóúñÑªº¿⌐¬½¼¡«»░▒▓│┤╡╢╖╕╣║╗╝╜╛┐" +
		"└┴┬├─┼╞╟╚╔╩╦╠═╬╧╨╤╥╙╘╒╓╫╪┘┌█▄▌▐▀" +
		"αßΓπΣσµτΦΘΩδ∞φε∩≡±≥≤⌠⌡÷≈°∙·√ⁿ²■\u00a0")
)

// decodeCP437 converts a CP437 name to UTF-8. ASCII is unchanged and NUL stays NUL (path
// validation rejects it).
func decodeCP437(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c >= 0x80:
			b.WriteRune(cp437High[c-0x80])
		case c >= 0x01 && c <= 0x1f:
			b.WriteRune(cp437Low[c-0x01])
		case c == 0x7f:
			b.WriteRune('⌂')
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}
