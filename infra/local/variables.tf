variable "aws_endpoint" {
  description = "Endpoint of the local AWS emulator, as seen from the host."
  type        = string
  default     = "http://localhost:4566"
}

variable "lambda_aws_endpoint" {
  description = "Endpoint of the local AWS emulator, as seen from Lambda containers."
  type        = string
  default     = "http://floci:4566"
}

variable "region" {
  description = "AWS region used by every resource."
  type        = string
  default     = "us-east-1"
}

variable "rental_id" {
  description = "The single Rental that self-registered Renters join."
  type        = string
  default     = "default"
}

variable "fleet_table" {
  description = "Name of the fleet DynamoDB table; must match FLEET_TABLE of the service."
  type        = string
  default     = "jemlih-fleet"
}

variable "event_bus_name" {
  description = "Name of the custom EventBridge bus; must match EVENT_BUS_NAME of the tests."
  type        = string
  default     = "jemlih"
}

variable "archive_buffer_seconds" {
  description = "How long Firehose buffers events before writing an archive object."
  type        = number
  default     = 60
}

variable "events_relay_zip" {
  description = "Path of the built events relay Lambda package."
  type        = string
  default     = "../../build/events-relay.zip"
}

variable "account_post_confirmation_zip" {
  description = "Path of the built post-confirmation Lambda package."
  type        = string
  default     = "../../build/account-postconfirm.zip"
}
