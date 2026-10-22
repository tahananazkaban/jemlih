# Event contracts

Every event on the `jemlih` bus is an [envelope](schemas/envelope.v1.json) whose `data` matches `schemas/<Type>.v<version>.json`. These files are the contract between services.

## Rules

- **Additive changes only.** A version may gain optional fields. Removing, renaming or retyping a field, or making it required, needs a new file with the next version.
- **Publish old versions until no consumer reads them.** Consumers name the versions they accept.
- **Consumers are idempotent on `event_id`.** Delivery is at least once, so the same event can arrive twice.
- **Money is integer rupiah.** Never fractions, never strings.
- **Times are UTC, RFC 3339.**
- **Facts, not commands.** Names are past tense (`UnitCreated`, not `CreateUnit`).

## Publishing

Services never call EventBridge directly. They add the event to the same DynamoDB transaction as the change it describes (an `EVENT#<event_id>` outbox item), and the relay forwards it from the table's stream to the bus. An event is therefore published if and only if its change was saved.
