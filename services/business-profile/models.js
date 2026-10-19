const mongoose = require('mongoose');

const BUSINESS_TYPES = ['weaver', 'dye-house', 'wholesaler', 'retailer'];
// GSTIN: 2-digit state code, PAN (5 letters, 4 digits, 1 letter), entity code, 'Z', checksum.
const GSTIN = /^[0-9]{2}[A-Z]{5}[0-9]{4}[A-Z][1-9A-Z]Z[0-9A-Z]$/;

const certificationSchema = new mongoose.Schema({
  name: { type: String, required: true, trim: true, maxlength: 200 },
  issuer: { type: String, trim: true, maxlength: 200 },
  documentUrl: { type: String, trim: true, maxlength: 2000 },
  addedAt: { type: Date, default: Date.now },
});

const profileSchema = new mongoose.Schema(
  {
    userId: { type: String, required: true, unique: true },
    businessName: { type: String, required: true, trim: true, maxlength: 200 },
    gstNumber: { type: String, trim: true, uppercase: true, match: [GSTIN, 'gstNumber is not a valid GSTIN'] },
    businessType: { type: String, required: true, enum: BUSINESS_TYPES },
    certifications: [certificationSchema],
    images: {
      type: [{ type: String, trim: true, maxlength: 2000 }],
      validate: [(v) => v.length <= 20, 'at most 20 images'],
    },
  },
  { timestamps: true },
);

profileSchema.index({ businessType: 1, businessName: 1 });

const Profile = mongoose.model('Profile', profileSchema);

function view(p) {
  return {
    userId: p.userId,
    businessName: p.businessName,
    gstNumber: p.gstNumber || null,
    businessType: p.businessType,
    certifications: p.certifications.map((c) => ({
      id: c._id.toString(),
      name: c.name,
      issuer: c.issuer || null,
      documentUrl: c.documentUrl || null,
      addedAt: c.addedAt,
    })),
    images: p.images,
    createdAt: p.createdAt,
    updatedAt: p.updatedAt,
  };
}

module.exports = { Profile, BUSINESS_TYPES, view };
