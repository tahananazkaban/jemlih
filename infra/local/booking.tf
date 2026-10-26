# Bookings, per-Unit calendars and outbox events of every Rental, keyed PK=RENTAL#<id>.
resource "aws_dynamodb_table" "booking" {
  name         = var.booking_table
  billing_mode = "PAY_PER_REQUEST"
  hash_key     = "PK"
  range_key    = "SK"

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

  ttl {
    attribute_name = "expires_at"
    enabled        = true
  }
}

output "booking_table" {
  value = aws_dynamodb_table.booking.name
}
