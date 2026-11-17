import { access, cp, mkdir, rm, writeFile } from 'node:fs/promises';
import { fileURLToPath } from 'node:url';
import sharp from 'sharp';

// App Store icons must have no alpha channel, even when every pixel is opaque.
// Generate from the shared artwork each sync so replacement artwork stays valid.
const iconSource = new URL(
  '../../frontend/src/lib/assets/chatto-icon-maskable.png',
  import.meta.url
);
const iconDestination = new URL(
  '../ios/App/App/Assets.xcassets/AppIcon.appiconset/AppIcon-512@2x.png',
  import.meta.url
);
await sharp(fileURLToPath(iconSource))
  .resize(1024, 1024)
  .flatten({ background: '#ffffff' })
  .removeAlpha()
  .png()
  .toFile(fileURLToPath(iconDestination));
const icon = await sharp(fileURLToPath(iconDestination)).metadata();
if (icon.width !== 1024 || icon.height !== 1024 || icon.hasAlpha) {
  throw new Error('The iOS app icon must be 1024x1024 with no alpha channel.');
}

// Android launcher and notification icons. The adaptive icon is 108dp with a
// 72dp visible area. Scale the maskable artwork so that its 80% safe zone
// fills the visible area: 72dp / 0.8 = 90dp.
const androidRes = new URL('../android/app/src/main/res/', import.meta.url);
const densities = { mdpi: 1, hdpi: 1.5, xhdpi: 2, xxhdpi: 3, xxxhdpi: 4 };
const notificationSource = new URL(
  '../../frontend/src/lib/assets/chatto-notification-badge.svg',
  import.meta.url
);
const androidOutput = (directory, name) =>
  fileURLToPath(new URL(`${directory}/${name}`, androidRes));
for (const [density, scale] of Object.entries(densities)) {
  const mipmap = `mipmap-${density}`;
  await mkdir(new URL(`${mipmap}/`, androidRes), { recursive: true });
  const layer = Math.round(108 * scale);
  const artwork = Math.round(90 * scale);
  const inset = Math.floor((layer - artwork) / 2);
  const foreground = await sharp(fileURLToPath(iconSource)).resize(artwork, artwork).png().toBuffer();
  await sharp({
    create: { width: layer, height: layer, channels: 4, background: { r: 0, g: 0, b: 0, alpha: 0 } }
  })
    .composite([{ input: foreground, left: inset, top: inset }])
    .png()
    .toFile(androidOutput(mipmap, 'ic_launcher_foreground.png'));

  // Themed (monochrome) icon layer: the notification silhouette in the safe zone.
  const monochromeSize = Math.round(48 * scale);
  const monochrome = await sharp(fileURLToPath(notificationSource), { density: 72 * scale * 2 })
    .resize(monochromeSize, monochromeSize)
    .png()
    .toBuffer();
  const monochromeInset = Math.floor((layer - monochromeSize) / 2);
  await sharp({
    create: { width: layer, height: layer, channels: 4, background: { r: 0, g: 0, b: 0, alpha: 0 } }
  })
    .composite([{ input: monochrome, left: monochromeInset, top: monochromeInset }])
    .png()
    .toFile(androidOutput(mipmap, 'ic_launcher_monochrome.png'));

  // Legacy icons for Android 7: full-bleed square and circle, 48dp.
  const legacy = Math.round(48 * scale);
  const square = await sharp(fileURLToPath(iconSource))
    .resize(legacy, legacy)
    .flatten({ background: '#ffffff' })
    .png()
    .toBuffer();
  await sharp(square).toFile(androidOutput(mipmap, 'ic_launcher.png'));
  const circle = Buffer.from(
    `<svg width="${legacy}" height="${legacy}"><circle cx="${legacy / 2}" cy="${legacy / 2}" r="${legacy / 2}"/></svg>`
  );
  await sharp(square)
    .ensureAlpha()
    .composite([{ input: circle, blend: 'dest-in' }])
    .png()
    .toFile(androidOutput(mipmap, 'ic_launcher_round.png'));

  // Notification small icon: white silhouette on transparency, 24dp.
  const drawable = `drawable-${density}`;
  await mkdir(new URL(`${drawable}/`, androidRes), { recursive: true });
  // Trim the SVG margin and fit the silhouette into 20dp of the 24dp canvas.
  const small = Math.round(24 * scale);
  const glyph = Math.round(20 * scale);
  const silhouette = await sharp(fileURLToPath(notificationSource), { density: 72 * scale * 4 })
    .trim()
    .resize(glyph, glyph, { fit: 'contain', background: { r: 0, g: 0, b: 0, alpha: 0 } })
    .png()
    .toBuffer();
  const smallInset = Math.floor((small - glyph) / 2);
  await sharp({
    create: { width: small, height: small, channels: 4, background: { r: 0, g: 0, b: 0, alpha: 0 } }
  })
    .composite([{ input: silhouette, left: smallInset, top: smallInset }])
    .png()
    .toFile(androidOutput(drawable, 'ic_stat_chatto.png'));
}

// Copy only build artifacts; the shared frontend source remains authoritative.
const source = new URL('../../frontend/build/', import.meta.url);
const destination = new URL('../www/', import.meta.url);
await access(new URL('200.html', source));
await rm(destination, { recursive: true, force: true });
await mkdir(destination, { recursive: true });
await cp(source, destination, { recursive: true });
// Capacitor serves index.html for SPA routes. Use SvelteKit's fallback shell.
await cp(new URL('200.html', source), new URL('index.html', destination));
await cp(
  new URL('../node_modules/@capacitor/core/LICENSE', import.meta.url),
  new URL('capacitor-license.txt', destination)
);
await cp(
  new URL('../node_modules/@capacitor/keyboard/LICENSE', import.meta.url),
  new URL('capacitor-keyboard-license.txt', destination)
);
await cp(
  new URL('../node_modules/@capacitor/push-notifications/LICENSE', import.meta.url),
  new URL('capacitor-push-notifications-license.txt', destination)
);

// The iOS shell rings incoming calls with a native alert sound, which follows
// the Ring/Silent switch and vibrates like a phone call. Web Audio in the
// webview cannot do this. Synthesize the same two-tone burst as the web
// ringtone (440 Hz + 480 Hz, two pulses in 1.2 s) as 16-bit mono PCM.
{
  const sampleRate = 22_050;
  const seconds = 1.2;
  const pulse = seconds / 2;
  const samples = Math.round(sampleRate * seconds);
  const data = Buffer.alloc(samples * 2);
  for (let i = 0; i < samples; i++) {
    const t = i / sampleRate;
    const inPulse = t % pulse;
    const on = pulse - 0.08;
    // 20 ms attack and 40 ms release avoid clicks at the pulse edges.
    const envelope =
      inPulse >= on ? 0 : Math.min(1, inPulse / 0.02, (on - inPulse) / 0.04);
    const value =
      0.45 * envelope * (Math.sin(2 * Math.PI * 440 * t) + Math.sin(2 * Math.PI * 480 * t));
    data.writeInt16LE(Math.round(Math.max(-1, Math.min(1, value)) * 32_767), i * 2);
  }
  const header = Buffer.alloc(44);
  header.write('RIFF', 0);
  header.writeUInt32LE(36 + data.length, 4);
  header.write('WAVEfmt ', 8);
  header.writeUInt32LE(16, 16);
  header.writeUInt16LE(1, 20);
  header.writeUInt16LE(1, 22);
  header.writeUInt32LE(sampleRate, 24);
  header.writeUInt32LE(sampleRate * 2, 28);
  header.writeUInt16LE(2, 32);
  header.writeUInt16LE(16, 34);
  header.write('data', 36);
  header.writeUInt32LE(data.length, 40);
  await writeFile(new URL('chatto-ringtone.wav', destination), Buffer.concat([header, data]));
}
