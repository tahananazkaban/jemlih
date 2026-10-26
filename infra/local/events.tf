# The bus every service publishes to through its outbox and the relay.
resource "aws_cloudwatch_event_bus" "main" {
  name = var.event_bus_name
}

# Every event, kept forever so later consumers can be built by replay.
resource "aws_s3_bucket" "event_archive" {
  bucket        = "jemlih-event-archive"
  force_destroy = true
}

resource "aws_iam_role" "firehose" {
  name = "jemlih-event-archive-firehose"

  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = "firehose.amazonaws.com" }
      Action    = "sts:AssumeRole"
    }]
  })
}

resource "aws_iam_role_policy" "firehose_s3" {
  name = "write-archive"
  role = aws_iam_role.firehose.id

  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect   = "Allow"
      Action   = ["s3:PutObject", "s3:GetBucketLocation", "s3:ListBucket"]
      Resource = [aws_s3_bucket.event_archive.arn, "${aws_s3_bucket.event_archive.arn}/*"]
    }]
  })
}

resource "aws_kinesis_firehose_delivery_stream" "event_archive" {
  name        = "jemlih-event-archive"
  destination = "extended_s3"

  extended_s3_configuration {
    role_arn            = aws_iam_role.firehose.arn
    bucket_arn          = aws_s3_bucket.event_archive.arn
    prefix              = "events/"
    error_output_prefix = "errors/"
    buffering_size      = 1
    buffering_interval  = var.archive_buffer_seconds

    # One event per line (JSON Lines), so replay and Athena can split the objects
    processing_configuration {
      enabled = true

      processors {
        type = "AppendDelimiterToRecord"
      }
    }
  }
}

resource "aws_iam_role" "eventbridge_firehose" {
  name = "jemlih-event-archive-rule"

  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = "events.amazonaws.com" }
      Action    = "sts:AssumeRole"
    }]
  })
}

resource "aws_iam_role_policy" "eventbridge_firehose" {
  name = "put-archive"
  role = aws_iam_role.eventbridge_firehose.id

  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect   = "Allow"
      Action   = ["firehose:PutRecord", "firehose:PutRecordBatch"]
      Resource = aws_kinesis_firehose_delivery_stream.event_archive.arn
    }]
  })
}

resource "aws_cloudwatch_event_rule" "archive_all" {
  name           = "archive-all"
  event_bus_name = aws_cloudwatch_event_bus.main.name
  event_pattern  = jsonencode({ source = [{ prefix = "jemlih." }] })
}

resource "aws_cloudwatch_event_target" "archive_all" {
  rule           = aws_cloudwatch_event_rule.archive_all.name
  event_bus_name = aws_cloudwatch_event_bus.main.name
  target_id      = "event-archive"
  arn            = aws_kinesis_firehose_delivery_stream.event_archive.arn
  role_arn       = aws_iam_role.eventbridge_firehose.arn
}

# Relay from service table streams to the bus; one function, one mapping per table.
locals {
  outbox_streams = {
    fleet    = aws_dynamodb_table.fleet.stream_arn
    booking  = aws_dynamodb_table.booking.stream_arn
    customer = aws_dynamodb_table.customer.stream_arn
  }
}

resource "aws_iam_role" "relay" {
  name = "jemlih-events-relay"

  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = "lambda.amazonaws.com" }
      Action    = "sts:AssumeRole"
    }]
  })
}

resource "aws_iam_role_policy" "relay" {
  name = "read-streams-put-events"
  role = aws_iam_role.relay.id

  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Effect   = "Allow"
        Action   = ["dynamodb:DescribeStream", "dynamodb:GetRecords", "dynamodb:GetShardIterator", "dynamodb:ListStreams"]
        Resource = values(local.outbox_streams)
      },
      {
        Effect   = "Allow"
        Action   = "events:PutEvents"
        Resource = aws_cloudwatch_event_bus.main.arn
      },
      {
        Effect   = "Allow"
        Action   = "sqs:SendMessage"
        Resource = aws_sqs_queue.relay_dlq.arn
      },
    ]
  })
}

resource "aws_lambda_function" "relay" {
  function_name    = "jemlih-events-relay"
  role             = aws_iam_role.relay.arn
  runtime          = "provided.al2023"
  handler          = "bootstrap"
  architectures    = ["x86_64"]
  filename         = var.events_relay_zip
  source_code_hash = filebase64sha256(var.events_relay_zip)
  timeout          = 30

  environment {
    variables = {
      EVENT_BUS_NAME      = aws_cloudwatch_event_bus.main.name
      JEMLIH_AWS_ENDPOINT = var.lambda_aws_endpoint
    }
  }
}

# Where stream records end up when the relay keeps failing, instead of being dropped.
resource "aws_sqs_queue" "relay_dlq" {
  name                      = "jemlih-events-relay-dlq"
  message_retention_seconds = 1209600
}

resource "aws_lambda_event_source_mapping" "relay" {
  for_each = local.outbox_streams

  function_name     = aws_lambda_function.relay.arn
  event_source_arn  = each.value
  starting_position = "TRIM_HORIZON"
  batch_size        = 10

  # The relay reports the first record it could not publish; retries start there.
  function_response_types        = ["ReportBatchItemFailures"]
  bisect_batch_on_function_error = true
  maximum_retry_attempts         = 5

  destination_config {
    on_failure {
      destination_arn = aws_sqs_queue.relay_dlq.arn
    }
  }

  # Only outbox inserts reach the relay; data items and TTL removals are filtered out.
  filter_criteria {
    filter {
      pattern = jsonencode({
        eventName = ["INSERT"]
        dynamodb  = { Keys = { SK = { S = [{ prefix = "EVENT#" }] } } }
      })
    }
  }
}

output "event_bus_name" {
  value = aws_cloudwatch_event_bus.main.name
}

output "event_archive_bucket" {
  value = aws_s3_bucket.event_archive.bucket
}
