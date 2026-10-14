param(
    [Parameter(Mandatory = $true)]
    [string]$BaseUrl
)

$ErrorActionPreference = 'Stop'
if (-not $env:TELEGRAM_BOT_TOKEN -or -not $env:TELEGRAM_WEBHOOK_SECRET) {
    throw 'Set TELEGRAM_BOT_TOKEN and TELEGRAM_WEBHOOK_SECRET in the environment.'
}
if ($env:TELEGRAM_WEBHOOK_SECRET -notmatch '^[A-Za-z0-9_-]{1,256}$') {
    throw 'Webhook secret must contain 1-256 characters: A-Z, a-z, 0-9, _ or -.'
}
$endpoint = [Uri]$BaseUrl
if (-not $endpoint.IsAbsoluteUri -or $endpoint.Scheme -ne 'https') {
    throw 'BaseUrl must be an absolute HTTPS URL.'
}
$body = @{
    url = $BaseUrl.TrimEnd('/') + '/webhook'
    secret_token = $env:TELEGRAM_WEBHOOK_SECRET
    allowed_updates = @('message', 'callback_query')
    max_connections = 1
} | ConvertTo-Json
try {
    $result = Invoke-RestMethod -Method Post `
        -Uri ('https://api.telegram.org/bot' + $env:TELEGRAM_BOT_TOKEN + '/setWebhook') `
        -ContentType 'application/json' -Body $body
    if (-not $result.ok) { throw 'Telegram rejected webhook registration.' }
} catch {
    throw 'Webhook registration failed. Check the token, HTTPS URL and Telegram connectivity. Credentials were not printed.'
}
Write-Output 'Webhook registered.'
