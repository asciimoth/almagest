StringContains outputs `true` when **input 1** contains **input 2** as one
continuous substring. The comparison is case-sensitive, and spaces and other
characters are significant. For example, `Almagest` contains `mage`, but it
does not contain `Mage`.

Each missing input has the value `""`. An empty **input 2** is contained in
every string, including an empty **input 1**, so the result is `true`. An empty
**input 1** does not contain a non-empty **input 2**. Removing a connection
restores the empty value for that input.

StringContains requests the current value when an input connects. It
recalculates when either input changes or disconnects, and sends an update only
when the Boolean result changes. A consumer receives the current result when
it connects or requests it. The node label and the read-only **Value** field
show the result.
