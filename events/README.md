# Event contracts

Every event on the `jemlih` bus is an [envelope](schemas/envelope.v1.json) whose `data` matches `schemas/<Type>.v<version>.json`. These files are the contract between services.

The envelope carries `event_id`, `type`, `version`, `occurred_at`, `rental_id`, `source` (the publishing service, e.g. `jemlih.fleet`) and `data`.

## Rules

- **Additive changes only.** A version may gain optional fields. Removing, renaming or retyping a field, or making it required, needs a new file with the next version.
- **Consumers ignore fields they do not know.** Schemas therefore allow extra properties; validating strictly would turn every additive change into a breaking one.
- **Publish old versions until no consumer reads them.** Consumers name the versions they accept.
- **Consumers are idempotent on `event_id`.** Delivery is at least once: a retried relay batch can publish the same event again.
- **Money is integer rupiah.** Never fractions, never strings.
- **Times are UTC, RFC 3339.**
- **Facts, not commands.** Names are past tense (`UnitCreated`, not `CreateUnit`).

## Publishing

Services never call EventBridge directly. They add the event to the same DynamoDB transaction as the change it describes (an `EVENT#<event_id>` outbox item), and the relay forwards it from the table's stream to the bus. An event is therefore published if and only if its change was saved.

A record the relay cannot publish is retried; after the retries it is moved, with its stream position, to the `jemlih-events-relay-dlq` queue instead of being dropped.

## Archive

Every event is written to `s3://jemlih-event-archive/events/` as JSON Lines: one EventBridge event per line, with the envelope in `detail`.
