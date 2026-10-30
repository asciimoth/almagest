StringUpper converts letters in [input 1](@port/left/input%201) to uppercase and
leaves other characters unchanged. It uses
[Unicode uppercase mappings](https://www.unicode.org/faq/casemap_charprop.html),
so it is not limited to ASCII. For example, `äbc 12` becomes `ÄBC 12`.

The conversion is not language- or locale-specific. It maps each character to at
most one uppercase character, so it does not apply expansions that require
multiple characters. A missing input has the value `""`; removing the input
connection restores that empty value. The input accepts one connection.

StringUpper requests the current String when the input connects. It recalculates
when the input changes or disconnects, and sends an update only when the
uppercase result changes. A consumer receives the current result when it
connects or requests it. The node label and the read-only **Value** field show
the result.
