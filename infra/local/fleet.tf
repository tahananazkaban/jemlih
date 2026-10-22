# Units, plate claims and outbox events of every Rental, keyed PK=RENTAL#<id>.
resource "aws_dynamodb_table" "fleet" {
  name         = var.fleet_table
  billing_mode = "PAY_PER_REQUEST"
  hash_key     = "PK"
  range_key    = "SK"

  # Feeds the events relay; NEW_IMAGE is enough to rebuild an outbox event.
  stream_enabled   = true
  stream_view_type = "NEW_IMAGE"

  attribute {
    name = "PK"
    type = "S"
  }

  attribute {
    name = "SK"
    type = "S"
  }

  # Removes relayed outbox items; data items never set expires_at.
  ttl {
    attribute_name = "expires_at"
    enabled        = true
  }
}

output "fleet_table" {
  value = aws_dynamodb_table.fleet.name
}
