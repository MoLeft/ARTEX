"use strict";

const VERSION = /^v0\.(\d{2})\.(\d{2})\.(\d{2})\.(?!000)(\d{3})(?:-beta([1-9]\d*)?)?$/;
const PLATFORMS = ["linux-amd64", "linux-arm64", "darwin-amd64", "darwin-arm64", "windows-amd64"];

function parseTag(tag) {
  const match = VERSION.exec(tag);
  if (!match) return null;
  const [, year, month, day, sequence, betaNumber] = match;
  const date = new Date(Date.UTC(2000 + Number(year), Number(month) - 1, Number(day)));
  if (date.getUTCFullYear() !== 2000 + Number(year) || date.getUTCMonth() !== Number(month) - 1 || date.getUTCDate() !== Number(day)) return null;
  const isBeta = tag.includes("-beta");
  return [Number(year), Number(month), Number(day), Number(sequence), isBeta ? 0 : 1, betaNumber === undefined ? 0 : Number(betaNumber)];
}

function selectLatestPreview(releases) {
  return releases.filter(release => !release.draft && release.published_at && parseTag(release.tag_name))
    .sort((a, b) => {
      const left = parseTag(a.tag_name);
      const right = parseTag(b.tag_name);
      for (let i = 0; i < left.length; i++) {
        if (left[i] !== right[i]) return right[i] - left[i];
      }
      return 0;
    })[0] || null;
}

function requireCompleteRelease(release) {
  if (!release || release.draft || !release.published_at || !parseTag(release.tag_name)) {
    throw new Error("没有已发布的有效版本");
  }
  const names = new Set((release.assets || []).map(asset => asset.name));
  for (const platform of PLATFORMS) {
    const name = `artex-${release.tag_name.slice(1)}-${platform}.zip`;
    if (!names.has(name)) throw new Error(`缺少 ${name}`);
  }
  if (!names.has("SHA256SUMS")) throw new Error("缺少 SHA256SUMS");
  return release;
}

module.exports = { parseTag, selectLatestPreview, requireCompleteRelease };
