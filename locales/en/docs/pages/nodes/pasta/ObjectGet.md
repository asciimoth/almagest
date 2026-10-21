Reads a named Object variable when it receives **Trigger** or when you trigger
the node manually. Set **Name** to the variable to read. If **Name** is empty, the
node uses `var`. Names are case-sensitive, and the node label shows the current
name. Applying a new **Name** does not read the variable.

## Trigger order

For each trigger, ObjectGet does these actions in order:

1. It reads the current object from the named variable.
2. It sends that object to all consumers connected to **Value**.
3. It sends a trigger to all consumers connected to **Trigger**.

This order lets the next triggered nodes use the object that ObjectGet has
read. The Trigger input can accept multiple trigger sources. The Value
and Trigger outputs can each supply multiple consumers.

ObjectGet does not monitor the variable. Trigger it again to read a later
value. A new **Value** connection receives nothing until the next trigger. If a
connected consumer requests the current value, ObjectGet repeats the last
object that it sent on that connection. It does not read the variable again
for the request. Changing **Name** clears these remembered values.

## Variable scope and object copies

ObjectGet and ObjectSet nodes with the same **Name** share the variable in the
current workspace. An Object variable is separate from a Boolean, Integer,
Float, or String variable with the same name. If no ObjectSet has written the
variable, ObjectGet reads `null`.

Object values are immutable. A consumer cannot change the stored variable
through a received value. It must create and send a replacement Object.

The shared value exists while at least one variable node refers to its name.
Removing or renaming the last such node clears the shared value. A later
ObjectGet then reads `null`. The workspace saves ObjectGet's name, but it does
not save the shared object as an independent global setting.
