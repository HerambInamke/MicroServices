const express = require('express');

const app = express();
const PORT = process.env.PORT || 3000;

app.use(express.json());

// Health check endpoint required by CONTRIBUTING.md
app.get('/health', (req, res) => {
  res.status(200).json({ status: 'ok', service: 'api-gateway' });
});

// Future routing logic goes here

app.listen(PORT, () => {
  console.log(`API Gateway listening on port ${PORT}`);
});