# One user pool for every role; the group decides the role and custom:rental_id the Rental.
resource "aws_cognito_user_pool" "main" {
  name                     = "jemlih"
  username_attributes      = ["email"]
  auto_verified_attributes = ["email"]

  password_policy {
    minimum_length                   = 10
    require_lowercase                = true
    require_uppercase                = true
    require_numbers                  = true
    require_symbols                  = false
    temporary_password_validity_days = 7
  }

  # Mutable so admins and triggers can set it; the web client is not allowed to write it.
  schema {
    name                = "rental_id"
    attribute_data_type = "String"
    mutable             = true

    string_attribute_constraints {
      min_length = 1
      max_length = 64
    }
  }

  lambda_config {
    post_confirmation = aws_lambda_function.account_post_confirmation.arn
  }
}

resource "aws_cognito_user_pool_client" "web" {
  name         = "web"
  user_pool_id = aws_cognito_user_pool.main.id

  explicit_auth_flows = ["ALLOW_USER_PASSWORD_AUTH", "ALLOW_USER_SRP_AUTH", "ALLOW_REFRESH_TOKEN_AUTH"]
  read_attributes     = ["email", "email_verified", "name", "custom:rental_id"]
  write_attributes    = ["email", "name"]

  prevent_user_existence_errors = "ENABLED"
}

resource "aws_cognito_user_group" "role" {
  for_each = toset(["owner", "staff", "renter"])

  name         = each.key
  user_pool_id = aws_cognito_user_pool.main.id
}

output "user_pool_id" {
  value = aws_cognito_user_pool.main.id
}

output "web_client_id" {
  value = aws_cognito_user_pool_client.web.id
}
