StringSplit divides [Text](@port/left/Text) at the first exact occurrence of
[Separator](@port/left/Separator). [Before](@port/right/Before) contains the
text before the match, and [After](@port/right/After) contains the text after
the match. The separator itself is not included. For example, splitting `a:b:c`
with `:` gives `a` through [Before](@port/right/Before) and `b:c` through
[After](@port/right/After).

The match is case-sensitive. If the separator is not found,
[Before](@port/right/Before) contains all of [Text](@port/left/Text) and
[After](@port/right/After) is `""`. An empty or missing
[Separator](@port/left/Separator) has the same result. A missing
[Text](@port/left/Text) is `""`, so both results are empty. Removing an input
connection restores its missing value.

StringSplit requests current values when its inputs connect. It recalculates
when either input changes or disconnects. It sends an update through
[Before](@port/right/Before) or [After](@port/right/After) only when that output
changes. A consumer receives the current value of its output when it connects or
requests it. The node label shows [Before](@port/right/Before) |
[After](@port/right/After), and the menu shows both values in read-only fields.
