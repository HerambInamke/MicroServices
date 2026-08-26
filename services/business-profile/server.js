const express = require('express');

const app = express();
const PORT = process.env.PORT || 8082;
const MONGO_URL = process.env.MONGO_URL || 'mongodb://root:rootpassword@mongodb:27017/admin';

app.use(express.json());

// Health check endpoint required by CONTRIBUTING.md
app.get('/health', (req, res) => {
  res.status(200).json({ status: 'ok', service: 'business-profile' });
});

// Future: Profile endpoints here
// POST /profiles
// GET /profiles/:id
// PUT /profiles/:id

app.listen(PORT, () => {
  console.log(`Business Profile Service listening on port ${PORT}`);
});