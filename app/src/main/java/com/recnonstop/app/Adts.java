package com.recnonstop.app;

import java.io.File;
import java.io.FileInputStream;
import java.io.InputStream;

/**
 * ADTS 头校验。
 *
 * 换 ADTS 的目的就是“文件被截断也能播”。上传前至少校验一下 syncword、
 * 采样率索引和声道数，避免把 0 字节/半截头文件喂给 ASR。
 */
public final class Adts {

    private Adts() {
    }

    public static boolean isValid(File f) {
        if (f == null || !f.exists() || f.length() < 7) {
            return false;
        }
        byte[] h = new byte[7];
        try (InputStream in = new FileInputStream(f)) {
            int n = in.read(h);
            if (n < 7) {
                return false;
            }
        } catch (Exception e) {
            return false;
        }
        int b0 = h[0] & 0xFF;
        int b1 = h[1] & 0xFF;
        int b2 = h[2] & 0xFF;
        int b3 = h[3] & 0xFF;
        if (b0 != 0xFF || (b1 & 0xF0) != 0xF0) {
            return false;
        }
        int srIndex = (b2 >> 2) & 0x0F;
        if (srIndex >= 13) {
            return false;
        }
        int channels = ((b2 & 0x01) << 2) | ((b3 >> 6) & 0x03);
        return channels > 0;
    }

    /** ADTS 头里的采样率索引 → Hz，失败返回 0。 */
    public static int sampleRate(File f) {
        int[] rates = {96000, 88200, 64000, 48000, 44100, 32000, 24000,
                22050, 16000, 12000, 11025, 8000, 7350};
        byte[] h = new byte[3];
        try (InputStream in = new FileInputStream(f)) {
            if (in.read(h) < 3) {
                return 0;
            }
        } catch (Exception e) {
            return 0;
        }
        int idx = ((h[2] & 0xFF) >> 2) & 0x0F;
        return idx < rates.length ? rates[idx] : 0;
    }
}
