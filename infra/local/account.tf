# Post-confirmation trigger that makes every self-registered user a Renter of the Rental.
resource "aws_iam_role" "account_lambda" {
  name = "jemlih-account-lambda"

  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = "lambda.amazonaws.com" }
      Action    = "sts:AssumeRole"
    }]
  })
}

resource "aws_iam_role_policy" "account_lambda_cognito" {
  name = "assign-renter"
  role = aws_iam_role.account_lambda.id

  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect   = "Allow"
      Action   = ["cognito-idp:AdminUpdateUserAttributes", "cognito-idp:AdminAddUserToGroup"]
      Resource = aws_cognito_user_pool.main.arn
    }]
  })
}

resource "aws_lambda_function" "account_post_confirmation" {
  function_name    = "jemlih-account-post-confirmation"
  role             = aws_iam_role.account_lambda.arn
  runtime          = "provided.al2023"
  handler          = "bootstrap"
  architectures    = ["x86_64"]
  filename         = var.account_post_confirmation_zip
  source_code_hash = filebase64sha256(var.account_post_confirmation_zip)
  timeout          = 10

  environment {
    variables = {
      RENTAL_ID           = var.rental_id
      JEMLIH_AWS_ENDPOINT = var.lambda_aws_endpoint
    }
  }
}

resource "aws_lambda_permission" "cognito_post_confirmation" {
  statement_id  = "AllowCognitoInvoke"
  action        = "lambda:InvokeFunction"
  function_name = aws_lambda_function.account_post_confirmation.function_name
  principal     = "cognito-idp.amazonaws.com"
  source_arn    = aws_cognito_user_pool.main.arn
}
