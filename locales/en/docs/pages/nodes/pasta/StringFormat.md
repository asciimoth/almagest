StringFormat builds one String from an ordered list of template parts. Add these
parts in the required output order. The initial template and result are empty.

- **Text** adds its text without changes. Its initial text is empty.
- **Value** adds an input for a variable value. Its initial name is `Value`, and
  its initial type is String.

A Value part can use String, Integer, Float, or Boolean data. The node converts
Integer and Float values to their standard text form. It converts Boolean
values to `true` or `false`. A disconnected input, or an input that has not
supplied a value, adds an empty string. Each value input accepts one connection.

The node joins all parts without an automatic separator. Add spaces,
punctuation, and line breaks to Text parts. For example, the parts `Hello `,
a String value named `Name`, and `!` can produce `Hello Ada!`.

Each Value name becomes its input name. A name can contain ASCII letters,
digits, spaces, hyphens, and underscores. The node removes other characters,
combines repeated spaces, and removes spaces, hyphens, or underscores from the
ends. It uses `Value` if the result is not a valid name. If names repeat, it
adds a number, such as `Value 2`.

Adding, removing, or reordering parts updates the inputs immediately. Renaming
a Value part keeps its existing connection. Changing its type removes a
connection that is not compatible with the new type. The node requests the
current value after an input connects and after the template changes. It
ignores an incoming value that does not match the selected type.

The node recalculates the result when an input value, input connection, or
template part changes. It sends an update only when the result changes. A new
consumer receives the current result immediately. The node also returns the
current result when a consumer requests it. The read-only **Result** field
shows the current String.
