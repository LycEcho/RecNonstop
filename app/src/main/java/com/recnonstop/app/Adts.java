package com.recnonstop.app;

import java.io.File;
import java.io.FileInputStream;
import java.io.InputStream;
import java.io.RandomAccessFile;

/**
 * ADTS 头校验。
 *
 * 换 ADTS 的目的就是“文件被截断也能播”。上传前至少校验一下 syncword、
 * 采样率索引和声道数，避免把 0 字节/半截头文件喂给 ASR。
 */
public final class Adts {

    private Adts() {
    }

    private static final int[] RATES = {96000, 88200, 64000, 48000, 44100, 32000,
            24000, 22050, 16000, 12000, 11025, 8000, 7350};

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
        int frameLength = frameLength(h);
        return frameLength > 0 && frameLength <= f.length();
    }

    /** 完整帧长度；不接受保留采样率、非法 layer 或只有帧头没有数据的文件。 */
    private static int frameLength(byte[] h) {
        int b0 = h[0] & 0xFF;
        int b1 = h[1] & 0xFF;
        int b2 = h[2] & 0xFF;
        int b3 = h[3] & 0xFF;
        if (b0 != 0xFF || (b1 & 0xF6) != 0xF0) {
            return 0;
        }
        int srIndex = (b2 >> 2) & 0x0F;
        if (srIndex >= 13) {
            return 0;
        }
        int channels = ((b2 & 0x01) << 2) | ((b3 >> 6) & 0x03);
        int length = ((b3 & 0x03) << 11) | ((h[4] & 0xFF) << 3)
                | ((h[5] & 0xE0) >> 5);
        int headerLength = (b1 & 1) != 0 ? 7 : 9;
        return channels > 0 && length > headerLength ? length : 0;
    }

    /** 异常退出后的 ADTS 保留完整帧，去掉写到一半的尾帧；失败时不删除原文件。 */
    public static boolean recover(File f) {
        if (f == null || !f.isFile()) return false;
        try (RandomAccessFile in = new RandomAccessFile(f, "rw")) {
            long size = in.length();
            long end = 0L;
            byte[] h = new byte[7];
            while (size - end >= h.length) {
                in.seek(end);
                in.readFully(h);
                int length = frameLength(h);
                if (length == 0 || length > size - end) break;
                end += length;
            }
            if (end == 0L) return false;
            if (end < size) in.setLength(end);
            return true;
        } catch (Exception e) {
            return false;
        }
    }

    /** ADTS 头里的采样率索引 → Hz，失败返回 0。 */
    public static int sampleRate(File f) {
        if (!isValid(f)) return 0;
        byte[] h = new byte[3];
        try (InputStream in = new FileInputStream(f)) {
            if (in.read(h) < 3) {
                return 0;
            }
        } catch (Exception e) {
            return 0;
        }
        int idx = ((h[2] & 0xFF) >> 2) & 0x0F;
        return idx < RATES.length ? RATES[idx] : 0;
    }
}
