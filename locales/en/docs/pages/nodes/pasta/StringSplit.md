StringSplit divides **Text** at the first exact occurrence of **Separator**.
**Before** contains the text before the match, and **After** contains the text
after the match. The separator itself is not included. For example, splitting
`a:b:c` with `:` gives `a` through **Before** and `b:c` through **After**.

The match is case-sensitive. If the separator is not found, **Before** contains
all of **Text** and **After** is `""`. An empty or missing **Separator** has the
same result. A missing **Text** is `""`, so both results are empty. Removing an
input connection restores its missing value.

StringSplit requests current values when its inputs connect. It recalculates
when either input changes or disconnects. It sends an update through **Before**
or **After** only when that output changes. A consumer receives the current
value of its output when it connects or requests it. The node label shows
`Before | After`, and the menu shows both values in read-only fields.
