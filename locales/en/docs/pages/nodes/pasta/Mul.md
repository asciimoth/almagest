Mul multiplies all connected numeric inputs. It starts with **input 1**. When
all input slots are connected, it adds the next numbered slot. When you remove
connections, it removes unused trailing slots until one free slot remains. Each
slot accepts one connection.

The first concrete Integer or Float input connection selects the calculation and
output type. This type does not change when you disconnect that input. If the
selected type is Integer, Float inputs are converted to integers by discarding
their fractional part. If the selected type is Float, Integer inputs are
converted to floating-point values. A generic connection can exist before a
concrete type is selected, but it does not select the type by itself.

Each missing value has the multiplication identity `1`. Mul therefore shows `1`
before it receives any factors, and an empty trailing slot does not change the
result. Removing an input removes that factor and recalculates the result.

Mul requests the current value when a concrete input connects. It recalculates
when an input value changes and sends an update only when the result changes.
After the output type is selected, a consumer receives the current result when
it connects or requests it. The node label and the read-only **Value** field
show the result.
