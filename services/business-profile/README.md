# business-profile

Node/Express · port **8082** · Mongo `business_profiles`.

One business profile per user: name, GSTIN (validated and upper-cased), business type, certifications and images.

## Endpoints

| Method & path | Who |
|---|---|
| `POST /profiles` | caller creates their own (409 if one exists; `userId` always comes from the token) |
| `GET /profiles?businessType=&q=&page=&limit=` | public |
| `GET /profiles/:userId` | public |
| `PATCH /profiles/:userId` | owner |
| `POST /profiles/:userId/certifications` `{name, issuer?, documentUrl?}` | owner |

## Configuration

`MONGO_URL`, `PORT`.

## Tests

```bash
MONGO_URL="mongodb://root:rootpassword@localhost:27017/bp_test?authSource=admin&directConnection=true" npm test   # throwaway database
```
