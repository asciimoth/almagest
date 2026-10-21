ObjectPacker builds an Object from typed inputs. You can also connect an
existing Object to **Base** and use it as a template. ObjectPacker builds a new
output and does not change the base object. If **Base** is not connected, it
starts with an empty Map or Vector, as selected by **Root**.

Paths are JSON arrays. A string selects a Map key, and a non-negative integer
selects a Vector index. For example, `["person","name"]` selects the `name` key
in the `person` Map, and `["items",0]` selects the first item in the `items`
Vector. Use **Containers** to explicitly declare a path as a Map or Vector when
the path cannot infer the required structure.

The selected **Root** and **Containers** types have priority over the shape in
**Base**. If a base subtree has an incompatible type, ObjectPacker replaces that
subtree with the configured container type.

Each entry in **Fields** creates one input named **In _Name_**. Select its type
from String, Integer, Float, Boolean, or Object, and select an operation:

- **set** writes the input value at **Path**. An unconnected field writes
  `null`. Object maps and vectors merge with matching base containers, so
  unrelated base values remain.
- **append** treats **Path** as a Vector and adds the input after its existing
  items. Append entries run in their order in **Fields**. Use `[]` to append to
  the root when **Root** is Vector.

**Delete paths** remove values from the base before fields are applied. A Map
path removes its key. A Vector path removes its indexed item and shifts later
items down. A field can write a replacement at a deleted path.

For example, with this base:

```json
{"person":{"id":1},"items":["base"],"legacy":true}
```

Set a String field to `Ada` at `["person","name"]`, append an Integer field with
value `7` at `["items"]`, and delete `["legacy"]`. The result contains
`{"person":{"id":1,"name":"Ada"},"items":["base",7]}`.

Paths must be valid JSON arrays. Keys cannot be empty. Indices must be whole,
non-negative numbers. Set, Container, and Delete paths cannot be empty. The
selected root type, declared containers, field paths, and append destinations
must agree. If validation fails, ObjectPacker keeps the previous configuration.

ObjectPacker requests current values when inputs connect. It rebuilds the object
when the base, a field, or the configuration changes, and sends an update only
if the result changed. A consumer receives the current object when it connects
or requests it. The object layout and its generated inputs are preserved when
you save the workspace or copy and paste the graph.
