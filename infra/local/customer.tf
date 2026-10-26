# Renter profiles, documents and verification of every Rental, keyed PK=RENTAL#<id>.
resource "aws_dynamodb_table" "customer" {
  name         = var.customer_table
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

# Identity documents; never public, read only through short-lived presigned URLs.
resource "aws_s3_bucket" "documents" {
  bucket        = var.document_bucket
  force_destroy = true
}

resource "aws_s3_bucket_public_access_block" "documents" {
  bucket = aws_s3_bucket.documents.id

  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

# Uploads wait under uploads/ until attached; abandoned ones disappear after a day.
resource "aws_s3_bucket_lifecycle_configuration" "documents" {
  bucket = aws_s3_bucket.documents.id

  rule {
    id     = "expire-abandoned-uploads"
    status = "Enabled"

    filter {
      prefix = "uploads/"
    }

    expiration {
      days = 1
    }
  }
}

# Browsers upload straight to the bucket, so it must accept cross-origin PUTs from the web app.
resource "aws_s3_bucket_cors_configuration" "documents" {
  bucket = aws_s3_bucket.documents.id

  cors_rule {
    allowed_methods = ["PUT", "GET"]
    allowed_origins = var.web_origins
    allowed_headers = ["content-type"]
    max_age_seconds = 3000
  }
}

output "customer_table" {
  value = aws_dynamodb_table.customer.name
}

output "document_bucket" {
  value = aws_s3_bucket.documents.bucket
}
