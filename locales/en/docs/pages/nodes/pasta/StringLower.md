StringLower converts letters in [input 1](@port/left/input%201) to lowercase and
leaves other characters unchanged. It uses
[Unicode lowercase mappings](https://www.unicode.org/faq/casemap_charprop.html),
so it is not limited to ASCII. For example, `ÄBC 12` becomes `äbc 12`.

The conversion is not language- or locale-specific. It maps each character to at
most one lowercase character, so it does not apply expansions that require
multiple characters. Do not use it as a complete locale-sensitive,
case-insensitive comparison. A missing input has the value `""`; removing the
input connection restores that empty value. The input accepts one connection.

StringLower requests the current string when the input connects. It recalculates
when the input changes or disconnects, and sends an update only when the
lowercase result changes. A consumer receives the current result when it
connects or requests it. The node label and the read-only **Value** field show
the result.
