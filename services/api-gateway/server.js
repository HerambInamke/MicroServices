const { createApp } = require('./app');

const PORT = process.env.PORT || 3000;

function requireEnv(name) {
  const v = process.env[name];
  if (!v) {
    console.error(`${name} is required`);
    process.exit(1);
  }
  return v;
}

const app = createApp({
  secret: requireEnv('JWT_SECRET'),
  rateLimitPerMinute: Number(process.env.RATE_LIMIT_PER_MINUTE) || 300,
  services: {
    auth: requireEnv('AUTH_URL'),
    profile: requireEnv('PROFILE_URL'),
    catalog: requireEnv('CATALOG_URL'),
    media: requireEnv('MEDIA_URL'),
    order: requireEnv('ORDER_URL'),
    ledger: requireEnv('LEDGER_URL'),
    qr: requireEnv('QR_URL'),
    counterfeit: requireEnv('COUNTERFEIT_URL'),
  },
});

app.listen(PORT, () => {
  console.log(`API Gateway listening on port ${PORT}`);
});
