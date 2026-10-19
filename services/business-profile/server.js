const mongoose = require('mongoose');
const { createApp } = require('./app');

const PORT = process.env.PORT || 8082;
const MONGO_URL = process.env.MONGO_URL;

if (!MONGO_URL) {
  console.error('MONGO_URL is required');
  process.exit(1);
}

mongoose
  .connect(MONGO_URL)
  .then(() => {
    createApp().listen(PORT, () => {
      console.log(`Business Profile Service listening on port ${PORT}`);
    });
  })
  .catch((err) => {
    console.error('could not connect to MongoDB:', err.message);
    process.exit(1);
  });
