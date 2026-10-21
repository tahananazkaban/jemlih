# Units and plate claims of every Rental, keyed PK=RENTAL#<id>.
resource "aws_dynamodb_table" "fleet" {
  name         = var.fleet_table
  billing_mode = "PAY_PER_REQUEST"
  hash_key     = "PK"
  range_key    = "SK"

  attribute {
    name = "PK"
    type = "S"
  }

  attribute {
    name = "SK"
    type = "S"
  }
}

output "fleet_table" {
  value = aws_dynamodb_table.fleet.name
}
